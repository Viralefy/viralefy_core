package paymentsclient

// Contract tests garantem que o shape JSON que o viralefy_payments emite
// continua compatível com o que este client desserializa.
//
// O bug "QR code sumiu no checkout" (2026-06-09) foi causado por uma única
// tag JSON divergente — `json:"extra"` aqui vs `json:"payment_extra"` no
// servidor. Sem teste, decode silenciou pra zero value e o front exibiu
// página em branco no PIX dinâmico.
//
// Estes testes simulam o servidor com httptest, batem fixtures contra os
// shapes esperados, e DETECTAM ANTES DO DEPLOY qualquer drift de envelope/tag.
//
// O lado simétrico vive em viralefy_payments/internal/interface/http/contract_test.go,
// que serializa as MESMAS structs do handler e compara byte-a-byte com as
// fixtures aqui — assim qualquer mudança requer atualizar os 2 lados juntos.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Viralefy/viralefy_core/internal/application"
)

// FixtureChargeResponse é o JSON que o servidor DEVE emitir em POST
// /internal/v1/charge — formato real de produção (Stripe + PIX dinâmico).
// Replicar em viralefy_payments/.../contract_test.go pra validação simétrica.
const FixtureChargeResponse = `{
  "provider": "abacatepay",
  "external_ref": "abc_tx_01H8XYZ1234",
  "payment_url": "https://api.abacatepay.com/v2/transparents/qr/abc_tx_01H8XYZ1234",
  "payment_extra": {
    "br_code": "00020126360014BR.GOV.BCB.PIX0114+5511999999999520400005303986540510.005802BR5910Test User6009Sao Paulo62070503***6304ABCD",
    "qr_code_image": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABAQMAAAAl21bKAAAAA1BMVEUAAACnej3aAAAAAXRSTlMAQObYZgAAAApJREFUCNdjYAAAAAIAAeIhvDMAAAAASUVORK5CYII="
  }
}`

// FixtureMethodsResponse é o envelope que GET /internal/v1/methods DEVE emitir.
// Note o envelope {"methods": [...]} — NÃO array raw.
const FixtureMethodsResponse = `{
  "methods": [
    {
      "gateway_id": "gw-stripe-live",
      "provider": "stripe",
      "name": "Cartão Stripe",
      "kind": "card",
      "charged_currency": "BRL",
      "charged_amount": "29.90",
      "charged_symbol": "R$",
      "settlement_currency": "BRL",
      "settlement_amount": "29.90",
      "settlement_symbol": "R$",
      "display_currency": "BRL",
      "display_amount": "29.90"
    },
    {
      "gateway_id": "gw-manual-pix",
      "provider": "manual_pix",
      "name": "PIX manual",
      "kind": "pix",
      "charged_currency": "BRL",
      "charged_amount": "29.90",
      "charged_symbol": "R$",
      "settlement_currency": "BRL",
      "settlement_amount": "29.90",
      "settlement_symbol": "R$",
      "display_currency": "BRL",
      "display_amount": "29.90"
    }
  ]
}`

func TestNewReturnsClient(t *testing.T) {
	c := New("http://127.0.0.1:8081/", "tok")
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.baseURL != "http://127.0.0.1:8081" {
		t.Fatalf("trailing slash not trimmed: %q", c.baseURL)
	}
	if c.Provider() != "remote" {
		t.Fatalf("Provider() = %q, want %q", c.Provider(), "remote")
	}
}

// TestCreateCharge_ContractRoundtrip simula o servidor emitindo
// FixtureChargeResponse e verifica que o client extrai TODOS os campos
// não-vazios. Se uma tag for renomeada, o campo correspondente vira "" e
// o teste falha — exatamente o tipo de drift que perdemos em prod uma vez.
func TestCreateCharge_ContractRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/charge" {
			t.Errorf("client called %q, expected /internal/v1/charge", r.URL.Path)
		}
		if r.Header.Get("X-Internal-Token") != "test-secret" {
			t.Errorf("X-Internal-Token missing or wrong: %q", r.Header.Get("X-Internal-Token"))
		}
		// Echo back the fixture so the client must decode the canonical shape.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, FixtureChargeResponse)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	out, err := c.CreateCharge(context.Background(), application.PaymentChargeInput{
		OrderID:     "ord-1",
		GatewayID:   "gw-abacatepay",
		Provider:    "abacatepay",
		Description: "Test",
		Amount:      "10.00",
		Currency:    "BRL",
		Customer:    application.PaymentCustomer{Name: "T", Email: "t@t.com"},
	})
	if err != nil {
		t.Fatalf("CreateCharge: %v", err)
	}

	// Campos que o front consome — qualquer um vazio = renderização quebrada.
	if out.ExternalRef == "" {
		t.Error("external_ref vazio — tag JSON divergiu?")
	}
	if out.PaymentURL == "" {
		t.Error("payment_url vazio — tag JSON divergiu?")
	}
	if len(out.Extra) == 0 {
		t.Fatal("payment_extra vazio — TAG DIVERGIU (foi assim que QR code sumiu uma vez)")
	}
	if out.Extra["br_code"] == "" {
		t.Error("payment_extra.br_code vazio")
	}
	if out.Extra["qr_code_image"] == "" {
		t.Error("payment_extra.qr_code_image vazio")
	}
}

// TestListMethods_ContractRoundtrip cobre o envelope {"methods":[...]}.
// Foi a causa do 500 em /v1/plans/{id}/payment-methods quando o client
// tentava unmarshal pra []PaymentMethodOption direto.
func TestListMethods_ContractRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/internal/v1/methods") {
			t.Errorf("client called %q, expected /internal/v1/methods*", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("plan_id") != "plan-xyz" {
			t.Errorf("plan_id query missing/wrong: %q", q.Get("plan_id"))
		}
		if q.Get("display_currency") != "BRL" {
			t.Errorf("display_currency query missing: %q", q.Get("display_currency"))
		}
		if q.Get("country") != "br" {
			t.Errorf("country query missing: %q", q.Get("country"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, FixtureMethodsResponse)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	methods, err := c.ListMethods(context.Background(), "plan-xyz", "BRL", "br")
	if err != nil {
		t.Fatalf("ListMethods: %v", err)
	}
	if len(methods) != 2 {
		t.Fatalf("expected 2 methods, got %d (envelope mismatch?)", len(methods))
	}
	stripe := methods[0]
	if stripe.GatewayID != "gw-stripe-live" || stripe.Provider != "stripe" || stripe.Kind != "card" {
		t.Errorf("stripe entry mal-decodificada: %+v", stripe)
	}
	if stripe.ChargedAmount == "" || stripe.DisplayAmount == "" || stripe.SettlementAmount == "" {
		t.Errorf("stripe amounts vazios: %+v", stripe)
	}
	pix := methods[1]
	if pix.Provider != "manual_pix" || pix.Kind != "pix" {
		t.Errorf("pix entry mal-decodificada: %+v", pix)
	}
}

// TestListMethods_RawArrayShape_Fails garante que se o servidor REGRIDIR e
// emitir array raw em vez do envelope, o client erra na hora (nem aceita
// silenciosamente). Documenta a expectativa do contrato.
func TestListMethods_RawArrayShape_Fails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"gateway_id":"x","provider":"stripe"}]`)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	_, err := c.ListMethods(context.Background(), "p", "BRL", "br")
	if err == nil {
		t.Fatal("esperava erro pra array raw, ListMethods aceitou silenciosamente (regrediria pro bug do 500)")
	}
}

// TestCreateCharge_MissingPaymentExtra documenta que o handler PODE omitir
// payment_extra (omitempty) em providers que não geram qrcode/wallet — Stripe
// hosted checkout não precisa. Garante que o client não panic-a nesse caso.
func TestCreateCharge_MissingPaymentExtra(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
		  "provider":"stripe",
		  "external_ref":"cs_live_abc",
		  "payment_url":"https://checkout.stripe.com/c/pay/cs_live_abc"
		}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	out, err := c.CreateCharge(context.Background(), application.PaymentChargeInput{
		OrderID: "ord", GatewayID: "gw", Provider: "stripe", Amount: "1", Currency: "BRL",
	})
	if err != nil {
		t.Fatalf("CreateCharge w/o extra: %v", err)
	}
	if out.PaymentURL == "" {
		t.Error("payment_url vazio em provider hosted")
	}
	if len(out.Extra) != 0 {
		t.Errorf("Extra deveria ser nil/empty quando omitempty no servidor, got %v", out.Extra)
	}
}

// TestCreateCharge_ServerError propaga corretamente HTTP 4xx do servidor
// (provider rejeitou input). Erros do upstream chegam ao monolito.
func TestCreateCharge_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"invalid_currency"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	_, err := c.CreateCharge(context.Background(), application.PaymentChargeInput{
		OrderID: "ord", GatewayID: "gw", Provider: "stripe", Amount: "1", Currency: "BRL",
	})
	if err == nil {
		t.Fatal("esperava erro pra HTTP 422")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("erro deveria mencionar status, got: %v", err)
	}
}

// TestChargeRequestShape garante que o body que o client manda continua
// batendo com o que o servidor espera. Roundtrip via JSON-marshal local.
func TestChargeRequestShape(t *testing.T) {
	in := chargeRequest{
		OrderID:     "ord-1",
		GatewayID:   "gw-1",
		Provider:    "stripe",
		Description: "Test",
		Amount:      "10.00",
		Currency:    "BRL",
		Customer:    customerPayload{Name: "T", Email: "t@t.com"},
		Config:      map[string]string{"k": "v"},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Cada campo crítico precisa aparecer com a tag esperada.
	for _, key := range []string{
		`"order_id":"ord-1"`,
		`"gateway_id":"gw-1"`,
		`"provider":"stripe"`,
		`"amount":"10.00"`,
		`"currency":"BRL"`,
		`"customer":{"name":"T","email":"t@t.com"}`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("chargeRequest body sem %s, body=%s", key, string(raw))
		}
	}
}
