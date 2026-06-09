package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

// sign monta um Stripe-Signature header sintético que o VerifyStripeWebhook
// aceita. helper compartilhado por vários testes pra montar fixtures.
func sign(body []byte, secret string, t int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d", t)
	mac.Write([]byte("."))
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}

func TestVerifyStripeWebhook_OK_singleV1(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"id":"evt_1","type":"checkout.session.completed"}`)
	hdr := sign(body, secret, time.Now().Unix())
	if err := VerifyStripeWebhook(body, hdr, secret); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestVerifyStripeWebhook_OK_multipleV1_roating(t *testing.T) {
	// Stripe envia múltiplos v1 durante key rotation. Qualquer um deve bater.
	secret := "whsec_new"
	body := []byte(`{}`)
	ts := time.Now().Unix()
	hdr := fmt.Sprintf("t=%d,v1=deadbeef,v1=%s,v1=baadf00d", ts,
		hex.EncodeToString(hmac.New(sha256.New, []byte(secret)).Sum(append([]byte(fmt.Sprintf("%d", ts)+"."), body...))))
	// Reconstruct properly: HMAC sum over ts.body
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d", ts)
	mac.Write([]byte("."))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))
	hdr = fmt.Sprintf("t=%d,v1=deadbeef,v1=%s,v1=baadf00d", ts, good)
	if err := VerifyStripeWebhook(body, hdr, secret); err != nil {
		t.Fatalf("expected one of multiple v1 to match, got %v", err)
	}
}

func TestVerifyStripeWebhook_InvalidSignature(t *testing.T) {
	body := []byte(`{}`)
	hdr := fmt.Sprintf("t=%d,v1=cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe", time.Now().Unix())
	if err := VerifyStripeWebhook(body, hdr, "whsec_test"); err == nil {
		t.Fatal("expected signature mismatch error")
	}
}

func TestVerifyStripeWebhook_ExpiredTimestamp(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{}`)
	// Anti-replay: 10min antigo cai fora do tolerance de 5min.
	old := time.Now().Add(-10 * time.Minute).Unix()
	hdr := sign(body, secret, old)
	if err := VerifyStripeWebhook(body, hdr, secret); err == nil {
		t.Fatal("expected timestamp tolerance rejection")
	} else if !strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("expected timestamp error, got %v", err)
	}
}

func TestVerifyStripeWebhook_FutureTimestamp_rejected(t *testing.T) {
	// Reloj-skew positivo: timestamp 10min no futuro também é fora.
	secret := "whsec_test"
	body := []byte(`{}`)
	future := time.Now().Add(10 * time.Minute).Unix()
	hdr := sign(body, secret, future)
	if err := VerifyStripeWebhook(body, hdr, secret); err == nil {
		t.Fatal("expected future timestamp rejection")
	}
}

func TestVerifyStripeWebhook_MissingSecret(t *testing.T) {
	if err := VerifyStripeWebhook([]byte(`{}`), "t=123,v1=abc", ""); err == nil {
		t.Fatal("expected missing-secret error")
	}
}

func TestVerifyStripeWebhook_MissingHeader(t *testing.T) {
	if err := VerifyStripeWebhook([]byte(`{}`), "", "whsec_test"); err == nil {
		t.Fatal("expected missing-header error")
	}
}

func TestVerifyStripeWebhook_MalformedHeader(t *testing.T) {
	cases := []string{
		"garbage",
		"t=,v1=abc",         // empty timestamp
		"t=123",             // no v1
		"v1=abc",            // no t
		"t=notanumber,v1=x", // ts not int
	}
	for _, hdr := range cases {
		t.Run(hdr, func(t *testing.T) {
			if err := VerifyStripeWebhook([]byte(`{}`), hdr, "whsec_test"); err == nil {
				t.Fatalf("expected error for malformed header %q", hdr)
			}
		})
	}
}

func TestParseStripeEvent_OK(t *testing.T) {
	body := []byte(`{
		"id":"evt_1",
		"type":"checkout.session.completed",
		"data":{"object":{"id":"cs_test_1","client_reference_id":"order-abc","payment_status":"paid"}}
	}`)
	ev, err := ParseStripeEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ev.IsPaid() {
		t.Fatal("expected IsPaid true for checkout.session.completed + paid")
	}
	if ev.OrderID() != "order-abc" {
		t.Fatalf("expected order id from client_reference_id, got %q", ev.OrderID())
	}
}

func TestParseStripeEvent_orderIDFallbackToMetadata(t *testing.T) {
	body := []byte(`{
		"id":"evt_1",
		"type":"checkout.session.completed",
		"data":{"object":{"id":"cs_1","payment_status":"paid","metadata":{"order_id":"order-meta"}}}
	}`)
	ev, _ := ParseStripeEvent(body)
	if ev.OrderID() != "order-meta" {
		t.Fatalf("expected metadata fallback, got %q", ev.OrderID())
	}
}

func TestStripeEvent_IsPaid_ignoresUnrelatedTypes(t *testing.T) {
	body := []byte(`{
		"id":"evt_2",
		"type":"payment_intent.created",
		"data":{"object":{"id":"pi_1","payment_status":"paid"}}
	}`)
	ev, _ := ParseStripeEvent(body)
	if ev.IsPaid() {
		t.Fatal("expected IsPaid false for non-checkout.session.completed type")
	}
}

func TestParseStripeEvent_rejectsMissingType(t *testing.T) {
	body := []byte(`{"id":"evt_1","data":{"object":{}}}`)
	if _, err := ParseStripeEvent(body); err == nil {
		t.Fatal("expected error for missing type")
	}
}

func TestVerifyStripeWebhook_ConstantTimeCompare_correctSigStillMatches(t *testing.T) {
	// Sanity: hmac.Equal é constant-time mas precisa de same-length input.
	// VerifyStripeWebhook usa hex.EncodeToString que dá sempre 64 chars pra
	// SHA-256 → safe.
	secret := "whsec_test"
	body := []byte(`payload`)
	ts := time.Now().Unix()
	hdr := sign(body, secret, ts)
	if err := VerifyStripeWebhook(body, hdr, secret); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestValidateStripeKey_AcceptsSecretAndRestricted(t *testing.T) {
	for _, k := range []string{
		"sk_live_abc",
		"sk_test_abc",
		"rk_live_abc",
		"rk_test_abc",
	} {
		if err := validateStripeKey(k); err != nil {
			t.Fatalf("expected ok for %q, got %v", k, err)
		}
	}
}

func TestValidateStripeKey_RejectsPublishable(t *testing.T) {
	err := validateStripeKey("pk_live_abc")
	if err == nil {
		t.Fatal("publishable key should be rejected")
	}
	if !strings.Contains(err.Error(), "publishable") {
		t.Fatalf("error should mention publishable, got %v", err)
	}
}

func TestValidateStripeKey_RejectsEmpty(t *testing.T) {
	if err := validateStripeKey(""); err == nil {
		t.Fatal("empty key should be rejected")
	}
}

func TestValidateStripeKey_RejectsUnknownPrefix(t *testing.T) {
	if err := validateStripeKey("whsec_xyz"); err == nil {
		t.Fatal("webhook secret should be rejected when used as API key")
	}
	if err := validateStripeKey("garbage"); err == nil {
		t.Fatal("garbage prefix should be rejected")
	}
}
