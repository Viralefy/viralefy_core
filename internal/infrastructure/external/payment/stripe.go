package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Viralefy/viralefy_core/internal/application"
)

// Stripe — Checkout Session API. Provider gera uma sessão hospedada por
// pedido e retorna a URL pra redirecionar o cliente. Sem libs externas: o
// stripe-go bate em 100+ deps; aqui chamamos a REST API direto.
//
// Config esperado:
//   secret_key   — sk_live_… (full secret) OU rk_live_… (restricted, recomendado)
//                  Test keys (sk_test_/rk_test_) também aceitas pra HML.
//                  PUBLISHABLE key (pk_…) é rejeitada — credential errada,
//                  vaza pro browser e não autoriza Checkout Sessions.
//   success_url  — opcional, default {siteURL}/account/orders/{order_id}
//   cancel_url   — opcional, default {siteURL}/checkout/cancelled
//   payment_method_types — opcional, default "card" (CSV: card,link,...)
//
// Restricted key (rk_live_…) é o caminho preferido em produção. Quando
// criar no dashboard da Stripe (Developers > API Keys > Create restricted
// key), conceda EXATAMENTE estas permissões — qualquer faltante quebra:
//
//   Checkout Sessions       Write
//   Prices                  Write   (price_data inline em line_items)
//   Products                Write   (product_data inline em line_items)
//
// Webhook signature usa webhook_secret separado (whsec_…), não a API key.
type Stripe struct {
	client  *http.Client
	siteURL string
}

func NewStripe(siteURL string) *Stripe {
	return &Stripe{
		client:  &http.Client{Timeout: 20 * time.Second},
		siteURL: strings.TrimRight(siteURL, "/"),
	}
}

func (*Stripe) Provider() string { return "stripe" }

// stripeKeyPrefixes aceita os 4 formatos válidos. pk_ (publishable) é
// PROIBIDO — é a key do front, vaza no bundle e não autoriza Checkout.
// Outros prefixos comuns que NÃO funcionam: whsec_ (webhook), price_, prod_.
var stripeKeyPrefixes = []string{"sk_live_", "sk_test_", "rk_live_", "rk_test_"}

func validateStripeKey(secret string) error {
	if secret == "" {
		return fmt.Errorf("stripe: missing secret_key in config (use sk_live_… or rk_live_…)")
	}
	if strings.HasPrefix(secret, "pk_") {
		return fmt.Errorf("stripe: publishable key (pk_…) provided as secret_key — use a secret (sk_…) or restricted (rk_…) key from Developers > API Keys")
	}
	for _, p := range stripeKeyPrefixes {
		if strings.HasPrefix(secret, p) {
			return nil
		}
	}
	return fmt.Errorf("stripe: secret_key has unrecognized prefix — expected one of sk_live_/sk_test_/rk_live_/rk_test_")
}

func (s *Stripe) CreateCharge(ctx context.Context, in application.PaymentChargeInput) (application.PaymentCharge, error) {
	secret := strings.TrimSpace(in.Config["secret_key"])
	if err := validateStripeKey(secret); err != nil {
		return application.PaymentCharge{}, err
	}

	successURL := strings.TrimSpace(in.Config["success_url"])
	if successURL == "" {
		successURL = s.siteURL + "/account/orders/" + in.OrderID
	}
	cancelURL := strings.TrimSpace(in.Config["cancel_url"])
	if cancelURL == "" {
		cancelURL = s.siteURL + "/checkout/cancelled?order_id=" + in.OrderID
	}
	methods := strings.TrimSpace(in.Config["payment_method_types"])
	if methods == "" {
		methods = "card"
	}

	cents, err := amountToMinorUnits(in.Amount)
	if err != nil {
		return application.PaymentCharge{}, fmt.Errorf("stripe: amount: %w", err)
	}

	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	form.Set("client_reference_id", in.OrderID)
	if in.Customer.Email != "" {
		form.Set("customer_email", in.Customer.Email)
	}
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(in.Currency))
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(cents, 10))
	form.Set("line_items[0][price_data][product_data][name]", in.Description)
	form.Set("metadata[order_id]", in.OrderID)
	// payment_method_types[]=card&payment_method_types[]=link
	for i, m := range strings.Split(methods, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		form.Set(fmt.Sprintf("payment_method_types[%d]", i), m)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return application.PaymentCharge{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(secret, "")

	resp, err := s.client.Do(req)
	if err != nil {
		return application.PaymentCharge{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return application.PaymentCharge{}, fmt.Errorf("stripe: HTTP %d: %s", resp.StatusCode, truncateStripe(string(body), 300))
	}
	var session struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return application.PaymentCharge{}, err
	}
	return application.PaymentCharge{
		ExternalRef: session.ID,
		PaymentURL:  session.URL,
		Extra: map[string]string{
			"method_kind": "card",
			"provider":    "stripe",
		},
	}, nil
}

// amountToMinorUnits converte "9.90" -> 990 (assumindo 2 decimals). Stripe
// usa minor units pra TODAS as moedas relevantes (BRL, USD, EUR, GBP).
// JPY/KRW seriam zero-decimal — quando precisar, mapeamos.
func amountToMinorUnits(amount string) (int64, error) {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return 0, fmt.Errorf("empty amount")
	}
	negative := false
	if strings.HasPrefix(amount, "-") {
		negative = true
		amount = amount[1:]
	}
	parts := strings.SplitN(amount, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	var dec int64
	if len(parts) == 2 {
		d := parts[1]
		if len(d) > 2 {
			d = d[:2]
		}
		for len(d) < 2 {
			d += "0"
		}
		dec, err = strconv.ParseInt(d, 10, 64)
		if err != nil {
			return 0, err
		}
	}
	out := whole*100 + dec
	if negative {
		out = -out
	}
	return out, nil
}

func truncateStripe(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// VerifyStripeWebhook valida a assinatura `Stripe-Signature` (HMAC SHA256
// do `timestamp.payload` usando o webhook secret). Tolera tolerance de 5min
// pra desvio de relógio. Aceita múltiplos v1=... no header (Stripe rotação).
//
// Header format:  t=1234567890,v1=hex,v1=hex,v0=...
//
// Algoritmo: signed_payload = timestamp + "." + payload_bytes
//            expected      = hmac_sha256(secret, signed_payload)
//            match qualquer um dos v1= no header com expected (constant-time)
func VerifyStripeWebhook(body []byte, signatureHeader, secret string) error {
	if secret == "" {
		return fmt.Errorf("stripe webhook: missing secret")
	}
	if signatureHeader == "" {
		return fmt.Errorf("stripe webhook: missing Stripe-Signature header")
	}
	var ts string
	v1s := []string{}
	for _, part := range strings.Split(signatureHeader, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			v1s = append(v1s, kv[1])
		}
	}
	if ts == "" || len(v1s) == 0 {
		return fmt.Errorf("stripe webhook: malformed Stripe-Signature header")
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("stripe webhook: invalid timestamp")
	}
	if abs64(time.Now().Unix()-tsInt) > 5*60 {
		return fmt.Errorf("stripe webhook: timestamp outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	expectedBytes := []byte(expected)
	for _, sig := range v1s {
		if hmac.Equal([]byte(sig), expectedBytes) {
			return nil
		}
	}
	return fmt.Errorf("stripe webhook: signature mismatch")
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// StripeEvent — shape mínimo do JSON do webhook Stripe que precisamos.
// Stripe envia uma estrutura grande; só nos importa identificar o tipo
// e o session_id / order_id quando é checkout.session.completed.
type StripeEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID                string            `json:"id"`
			ClientReferenceID string            `json:"client_reference_id"`
			Metadata          map[string]string `json:"metadata"`
			PaymentStatus     string            `json:"payment_status"`
		} `json:"object"`
	} `json:"data"`
}

func (e StripeEvent) IsPaid() bool {
	// checkout.session.completed sinaliza intent + payment_intent succeeded.
	// payment_intent.succeeded também serve mas chega em rotas paralelas;
	// pra evitar double-fire confiamos no completed (idempotência fica no
	// PaymentReceiver via order.status guard).
	if e.Type != "checkout.session.completed" {
		return false
	}
	return e.Data.Object.PaymentStatus == "paid" || e.Data.Object.PaymentStatus == ""
}

// OrderID resolve o id do pedido a partir do event. Priorizamos
// client_reference_id (setado pelo CreateCharge); fallback metadata.order_id.
func (e StripeEvent) OrderID() string {
	if id := strings.TrimSpace(e.Data.Object.ClientReferenceID); id != "" {
		return id
	}
	return strings.TrimSpace(e.Data.Object.Metadata["order_id"])
}

// ParseStripeEvent decodifica o JSON do webhook. Retorna ErrInvalidInput
// se não é JSON válido ou estrutura mínima.
func ParseStripeEvent(body []byte) (StripeEvent, error) {
	var ev StripeEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return ev, fmt.Errorf("stripe webhook: parse: %w", err)
	}
	if ev.Type == "" {
		return ev, fmt.Errorf("stripe webhook: missing type")
	}
	return ev, nil
}
