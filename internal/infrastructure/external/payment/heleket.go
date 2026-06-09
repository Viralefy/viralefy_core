package payment

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Viralefy/viralefy_core/internal/application"
)

// Heleket (https://heleket.com) — pagamento em cripto (USDT/BTC/etc).
// API: POST {base}/v1/payment com headers:
//
//	merchant: <merchant_id>
//	sign: md5( base64(json_body) + payment_api_key )  (assinatura padrão Heleket)
//
// Body: amount, currency, order_id, url_callback (opcional), url_success.
// Config esperada (via gateway.config):
//
//	merchant_id, api_key, base_url (opcional), url_callback (opcional), url_success (opcional)
type Heleket struct{ client *http.Client }

func NewHeleket() *Heleket { return &Heleket{client: &http.Client{Timeout: 15 * time.Second}} }

func (Heleket) Provider() string { return "heleket" }

type heleketReq struct {
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	OrderID     string `json:"order_id"`
	URLCallback string `json:"url_callback,omitempty"`
	URLSuccess  string `json:"url_success,omitempty"`
}

type heleketResp struct {
	Result struct {
		UUID          string `json:"uuid"`
		OrderID       string `json:"order_id"`
		Amount        string `json:"amount"`
		PaymentAmount string `json:"payment_amount"`
		PayerAmount   string `json:"payer_amount"`
		Currency      string `json:"currency"`
		PayerCurrency string `json:"payer_currency"`
		Address       string `json:"address"`
		Network       string `json:"network"`
		URL           string `json:"url"`
		ExpiredAt     int64  `json:"expired_at"`
		Status        string `json:"status"`
	} `json:"result"`
	State int `json:"state"`
}

func (h *Heleket) CreateCharge(ctx context.Context, in application.PaymentChargeInput) (application.PaymentCharge, error) {
	merchant := strings.TrimSpace(in.Config["merchant_id"])
	apiKey := strings.TrimSpace(in.Config["api_key"])
	if merchant == "" || apiKey == "" {
		return application.PaymentCharge{}, fmt.Errorf("heleket: faltando config merchant_id e api_key")
	}
	baseURL := strings.TrimRight(in.Config["base_url"], "/")
	if baseURL == "" {
		baseURL = "https://api.heleket.com"
	}

	body, _ := json.Marshal(heleketReq{
		Amount:      in.Amount,
		Currency:    strings.ToUpper(in.Currency),
		OrderID:     in.OrderID,
		URLCallback: in.Config["url_callback"],
		URLSuccess:  in.Config["url_success"],
	})

	// Assinatura: md5( base64(body) + api_key )
	sum := md5.Sum([]byte(base64.StdEncoding.EncodeToString(body) + apiKey))
	sign := fmt.Sprintf("%x", sum)

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+"/v1/payment", bytes.NewReader(body))
	if err != nil {
		return application.PaymentCharge{}, err
	}
	req.Header.Set("merchant", merchant)
	req.Header.Set("sign", sign)
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return application.PaymentCharge{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return application.PaymentCharge{}, fmt.Errorf("heleket: status %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	var out heleketResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return application.PaymentCharge{}, fmt.Errorf("heleket: resposta inválida: %w", err)
	}
	r := out.Result
	return application.PaymentCharge{
		ExternalRef: r.UUID,
		PaymentURL:  r.URL,
		Extra: map[string]string{
			"address":        r.Address,
			"network":        r.Network,
			"payer_currency": r.PayerCurrency,
			"payer_amount":   r.PayerAmount,
			"status":         r.Status,
		},
	}, nil
}
