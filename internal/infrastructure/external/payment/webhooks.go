package payment

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// VerifyWooviWebhook valida a assinatura HMAC-SHA256 do webhook.
// Header: x-webhook-signature, valor = base64( HMAC_SHA256(body, secret) ).
// Secret é o "webhook_secret" cadastrado no gateway.config.
func VerifyWooviWebhook(body []byte, signatureHeader, secret string) error {
	signatureHeader = strings.TrimSpace(signatureHeader)
	if signatureHeader == "" {
		return fmt.Errorf("woovi: assinatura ausente")
	}
	if secret == "" {
		return fmt.Errorf("woovi: webhook_secret não configurado no gateway")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signatureHeader)) {
		return fmt.Errorf("woovi: assinatura inválida")
	}
	return nil
}

// WooviEvent extrai o evento e o external_ref (identifier da charge).
type WooviEvent struct {
	Event  string `json:"event"`
	Charge struct {
		Identifier    string `json:"identifier"`
		CorrelationID string `json:"correlationID"`
		Status        string `json:"status"`
	} `json:"charge"`
}

func ParseWooviEvent(body []byte) (*WooviEvent, error) {
	var e WooviEvent
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// IsPaid retorna true se este evento Woovi indica pagamento confirmado.
func (e WooviEvent) IsPaid() bool {
	return strings.EqualFold(e.Charge.Status, "COMPLETED") ||
		strings.EqualFold(e.Charge.Status, "CONFIRMED") ||
		strings.Contains(strings.ToUpper(e.Event), "COMPLETED")
}

// VerifyHeleketWebhook valida a assinatura embutida no body:
// sign = md5( base64(body_sem_o_campo_sign) + api_key ).
func VerifyHeleketWebhook(body []byte, apiKey string) error {
	if apiKey == "" {
		return fmt.Errorf("heleket: api_key não configurado")
	}
	// Decodifica como map genérico, remove sign, re-serializa, recalcula md5.
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("heleket: payload inválido: %w", err)
	}
	signRaw, ok := payload["sign"]
	if !ok {
		return fmt.Errorf("heleket: campo sign ausente")
	}
	sign, _ := signRaw.(string)
	if sign == "" {
		return fmt.Errorf("heleket: sign vazio")
	}
	delete(payload, "sign")
	clean, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sum := md5.Sum([]byte(base64.StdEncoding.EncodeToString(clean) + apiKey))
	expected := fmt.Sprintf("%x", sum)
	if expected != sign {
		return fmt.Errorf("heleket: assinatura inválida")
	}
	return nil
}

// HeleketEvent é o subconjunto que interessa pro receiver.
type HeleketEvent struct {
	UUID    string `json:"uuid"`
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
	Type    string `json:"type"`
}

func ParseHeleketEvent(body []byte) (*HeleketEvent, error) {
	var e HeleketEvent
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (e HeleketEvent) IsPaid() bool {
	s := strings.ToLower(strings.TrimSpace(e.Status))
	return s == "paid" || s == "paid_over"
}
