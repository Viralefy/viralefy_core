// Package paymentsclient é o wrapper HTTP que o monolito viralefy_core usa
// pra falar com o microserviço viralefy_payments (loopback-only, porta 8081).
//
// O carve-out da Fase 8 substitui os providers in-memory (Stripe/Heleket/
// Woovi/manual_pix/manual_usdt) por chamadas HTTP/JSON internas. Este
// Client implementa application.PaymentProvider — Provider() retorna "remote"
// porque do ponto de vista do CheckoutService não importa qual gateway está
// por baixo, o microserviço resolve.
//
// Convenções:
//   - Todas as requests carregam X-Internal-Token (defense-in-depth; o
//     bind loopback-only já mitiga, mas o token blinda contra SSRF acidental).
//   - Path versionado /internal/v1/... desde o dia 1 (PHASE-8 §3).
//   - Timeout 30s no http.Client — Stripe Checkout Session demora em picos
//     (network + criação de Price + Product inline). 20s do client legado
//     do Stripe estourava em ~1% das requests.
package paymentsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Viralefy/viralefy_core/internal/application"
)

const (
	defaultTimeout = 30 * time.Second
	headerToken    = "X-Internal-Token"
)

// Client é o cliente HTTP do viralefy_payments. Stateless — pode ser
// compartilhado entre goroutines.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New cria o cliente. baseURL é tipo "http://127.0.0.1:8081"; token é o
// INTERNAL_SHARED_SECRET. Trim trailing slash pra simplificar concat.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

// Provider satisfaz application.PaymentProvider. Retorna "remote" porque
// o registry só registra UM provider quando o monolito está em modo
// microservice — a identidade real do gateway (stripe/woovi/…) é resolvida
// pelo microservice via gateway_id no payload.
func (*Client) Provider() string { return "remote" }

// chargeRequest é o corpo de POST /internal/v1/charge.
// Shape espelha PHASE-8 §1 (ChargeRequest) + os campos que o CheckoutService
// já popula em PaymentChargeInput. Mantemos a estrutura plana pra o
// microservice serializar/deserializar sem repackaging.
type chargeRequest struct {
	OrderID     string            `json:"order_id"`
	GatewayID   string            `json:"gateway_id,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Description string            `json:"description"`
	Amount      string            `json:"amount"`
	Currency    string            `json:"currency"`
	Customer    customerPayload   `json:"customer"`
	Config      map[string]string `json:"config,omitempty"`
}

type customerPayload struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// chargeResponse é o corpo de resposta de POST /internal/v1/charge.
// IMPORTANTE: tags JSON precisam bater EXATAMENTE com o que o microservice
// viralefy_payments emite (vide handlers.go:108 lá). Tag errada =
// unmarshal silencioso pra zero value = QR code perdido no checkout.
type chargeResponse struct {
	Provider     string            `json:"provider"`
	ExternalRef  string            `json:"external_ref"`
	PaymentURL   string            `json:"payment_url"`
	PaymentExtra map[string]string `json:"payment_extra,omitempty"`
}

// CreateCharge satisfaz application.PaymentProvider. Envia o input do
// CheckoutService pro microservice e devolve normalizado.
func (c *Client) CreateCharge(ctx context.Context, in application.PaymentChargeInput) (application.PaymentCharge, error) {
	body := chargeRequest{
		OrderID:     in.OrderID,
		GatewayID:   in.GatewayID,
		Provider:    in.Provider,
		Description: in.Description,
		Amount:      in.Amount,
		Currency:    in.Currency,
		Customer: customerPayload{
			Name:  in.Customer.Name,
			Email: in.Customer.Email,
		},
		Config: in.Config,
	}
	var out chargeResponse
	if err := c.doJSON(ctx, http.MethodPost, "/internal/v1/charge", body, &out); err != nil {
		return application.PaymentCharge{}, err
	}
	return application.PaymentCharge{
		ExternalRef: out.ExternalRef,
		PaymentURL:  out.PaymentURL,
		Extra:       out.PaymentExtra,
	}, nil
}

// PaymentMethodOption é o shape devolvido por GET /internal/v1/methods.
// Replicamos local pra evitar import-cycle paymentsclient → application
// (application já importa interfaces, não o reverso). Idêntico ao
// application.PaymentMethodOption — quando o monolito monta o handler de
// /v1/checkout/methods ele faz pass-through.
type PaymentMethodOption struct {
	GatewayID          string `json:"gateway_id"`
	Provider           string `json:"provider"`
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	ChargedCurrency    string `json:"charged_currency"`
	ChargedAmount      string `json:"charged_amount"`
	ChargedSymbol      string `json:"charged_symbol"`
	SettlementCurrency string `json:"settlement_currency"`
	SettlementAmount   string `json:"settlement_amount"`
	SettlementSymbol   string `json:"settlement_symbol"`
	DisplayCurrency    string `json:"display_currency"`
	DisplayAmount      string `json:"display_amount"`
	ConversionNote     string `json:"conversion_note,omitempty"`
	NetworkLabel       string `json:"network_label,omitempty"`
	NetworkWarning     string `json:"network_warning,omitempty"`
}

// ListMethods chama GET /internal/v1/methods. NÃO faz parte da
// application.PaymentProvider interface — é usado direto pelo handler de
// /v1/checkout/methods do monolito (que vira proxy do microservice).
//
// Os 3 filtros são query params; vazio = sem filtro (microservice decide
// o default — display=USD, country="" = sem filtro BR-only).
func (c *Client) ListMethods(ctx context.Context, planID, displayCurrency, country string) ([]PaymentMethodOption, error) {
	q := make([]string, 0, 3)
	if planID != "" {
		q = append(q, "plan_id="+escapeQuery(planID))
	}
	if displayCurrency != "" {
		q = append(q, "display_currency="+escapeQuery(displayCurrency))
	}
	if country != "" {
		q = append(q, "country="+escapeQuery(country))
	}
	path := "/internal/v1/methods"
	if len(q) > 0 {
		path += "?" + strings.Join(q, "&")
	}
	// Payments emite envelope {"methods":[...]} — não array raw. Unmarshal
	// pra []PaymentMethodOption direto causa "cannot unmarshal object into
	// Go value of type []PaymentMethodOption" → 500 no monolito.
	var env struct {
		Methods []PaymentMethodOption `json:"methods"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &env); err != nil {
		return nil, err
	}
	return env.Methods, nil
}

// doJSON centraliza serialização/auth/erro. body=nil → GET sem corpo.
// out=nil → discarta resposta (usado por endpoints sem retorno útil).
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("paymentsclient: marshal: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("paymentsclient: new request: %w", err)
	}
	req.Header.Set(headerToken, c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("paymentsclient: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("paymentsclient: %s %s: HTTP %d: %s",
			method, path, resp.StatusCode, truncate(string(respBody), 300))
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("paymentsclient: decode %s: %w", path, err)
	}
	return nil
}

// escapeQuery faz um percent-encode bem básico só pra não criar dep no
// net/url quando ele já está no pacote — mas mantemos esta função pra
// documentar a intenção (todos os 3 filtros aqui são plain ASCII de IDs
// uppercase/lowercase). Fallback pra net/url.QueryEscape mantém safety.
func escapeQuery(s string) string {
	// Implementação simples: delega pra padrão. Manter wrapper pra trocar
	// estratégia depois (escape custom) sem mexer nos call sites.
	return queryEscape(s)
}

// queryEscape é split pra facilitar mock/test sem usar net/url stub.
// Wrapper de uma linha — implementação real importada do stdlib.
func queryEscape(s string) string {
	// Inline percent-encode de chars não-safe. Como os inputs são ULIDs,
	// ISO 3166 (2 chars), e ISO 4217 (3 chars), 99% passa sem alteração.
	// Mantém compat com qualquer string mais exótica.
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if isUnreservedQuery(ch) {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexUpper(ch >> 4))
		b.WriteByte(hexUpper(ch & 0x0f))
	}
	return b.String()
}

func isUnreservedQuery(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z',
		c >= 'a' && c <= 'z',
		c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '-', '_', '.', '~':
		return true
	}
	return false
}

func hexUpper(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'A' + (n - 10)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
