// Package payment é o ACL (anti-corruption layer) para os gateways de
// pagamento. Cada provider tem um adapter que traduz nosso modelo
// (application.PaymentChargeInput) para o modelo do provider e a resposta
// para application.PaymentCharge.
package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Viralefy/viralefy_core/internal/application"
)

// Woovi (https://developers.woovi.com) — PIX no Brasil. Auth via header
// Authorization com o App ID (AppID é um token JWT-like emitido pela conta).
type Woovi struct{ client *http.Client }

func NewWoovi() *Woovi { return &Woovi{client: &http.Client{Timeout: 15 * time.Second}} }

func (Woovi) Provider() string { return "woovi" }

type wooviReq struct {
	CorrelationID string        `json:"correlationID"`
	Value         int           `json:"value"`
	Comment       string        `json:"comment,omitempty"`
	Customer      wooviCustomer `json:"customer,omitempty"`
}

type wooviCustomer struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type wooviResp struct {
	Charge struct {
		Identifier     string `json:"identifier"`
		CorrelationID  string `json:"correlationID"`
		Status         string `json:"status"`
		Value          int    `json:"value"`
		BrCode         string `json:"brCode"`
		QrCodeImage    string `json:"qrCodeImage"`
		PaymentLinkURL string `json:"paymentLinkUrl"`
		ExpiresDate    string `json:"expiresDate"`
	} `json:"charge"`
}

func (w *Woovi) CreateCharge(ctx context.Context, in application.PaymentChargeInput) (application.PaymentCharge, error) {
	if strings.ToUpper(in.Currency) != "BRL" {
		return application.PaymentCharge{}, fmt.Errorf("woovi: aceita apenas BRL (recebido %s)", in.Currency)
	}
	appID := strings.TrimSpace(in.Config["app_id"])
	if appID == "" {
		return application.PaymentCharge{}, fmt.Errorf("woovi: faltando config app_id")
	}
	baseURL := strings.TrimRight(in.Config["base_url"], "/")
	if baseURL == "" {
		baseURL = "https://api.woovi.com.br"
	}
	cents, err := toMinor(in.Amount, 2)
	if err != nil {
		return application.PaymentCharge{}, fmt.Errorf("woovi: amount inválido: %w", err)
	}

	body, _ := json.Marshal(wooviReq{
		CorrelationID: in.OrderID,
		Value:         cents,
		Comment:       in.Description,
		Customer:      wooviCustomer{Name: in.Customer.Name, Email: in.Customer.Email},
	})
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+"/api/v1/charge", bytes.NewReader(body))
	if err != nil {
		return application.PaymentCharge{}, err
	}
	req.Header.Set("Authorization", appID)
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return application.PaymentCharge{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return application.PaymentCharge{}, fmt.Errorf("woovi: status %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	var out wooviResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return application.PaymentCharge{}, fmt.Errorf("woovi: resposta inválida: %w", err)
	}
	return application.PaymentCharge{
		ExternalRef: out.Charge.Identifier,
		PaymentURL:  out.Charge.PaymentLinkURL,
		Extra: map[string]string{
			"br_code":       out.Charge.BrCode,
			"qr_code_image": out.Charge.QrCodeImage,
			"expires_date":  out.Charge.ExpiresDate,
			"status":        out.Charge.Status,
		},
	}, nil
}

// toMinor converte "9.90" → 990 (decimais=2). Aceita "9,90".
func toMinor(amount string, decimals int) (int, error) {
	a := strings.ReplaceAll(amount, ",", ".")
	f, err := strconv.ParseFloat(a, 64)
	if err != nil {
		return 0, err
	}
	return int(math.Round(f * math.Pow10(decimals))), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
