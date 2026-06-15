package senderclient

// Contract tests do senderclient. Espelham viralefy_sender/.../interface/http/send.go.
//
// Bugs históricos cobertos:
//   - "checkout: send email failed" — sender_microservice rejeitava raw
//     subject+body sem Template name. Fix: aceitar Template="" como
//     passthrough usando Subject+HTMLBody/TextBody. Teste garante que esse
//     payload é montado certo.
//   - SendTemplate sem TextBody nem HTMLBody: validar que o sender pode usar
//     só template+vars.
//
// Lado simétrico: viralefy_sender/internal/interface/http/contract_test.go
// re-serializa FixtureSendResponse e bate byte-a-byte.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Viralefy/viralefy_core/internal/application"
)

// FixtureSendResponse é o JSON que /internal/v1/send DEVE emitir.
// status="queued" é canonical pra enqueue OK; "failed" pra erro de provider.
const FixtureSendResponse = `{
  "status": "queued",
  "attempt_id": "att_01H8XYZ1234"
}`

func TestNewReturnsClient(t *testing.T) {
	c := New("http://127.0.0.1:8082/", "tok")
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.baseURL != "http://127.0.0.1:8082" {
		t.Fatalf("trailing slash not trimmed: %q", c.baseURL)
	}
}

// TestSend_RawPassthroughShape valida o caso legado (sem template,
// subject+html_body/text_body brutos). Era o único modo antes do
// sender_microservice; precisa continuar funcionando indefinidamente
// porque o monolith ainda usa em fluxos sem template registrado.
func TestSend_RawPassthroughShape(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/send" {
			t.Errorf("client called %q, expected /internal/v1/send", r.URL.Path)
		}
		if r.Header.Get("X-Internal-Token") != "test-secret" {
			t.Errorf("X-Internal-Token missing/wrong: %q", r.Header.Get("X-Internal-Token"))
		}
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, FixtureSendResponse)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	err := c.Send(context.Background(), application.EmailMessage{
		To:       "user@example.com",
		Subject:  "Test subject",
		HTMLBody: "<p>HTML</p>",
		TextBody: "Plain",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Decode pra struct local pra evitar drama com HTMLEscape (json.Marshal
	// vira < em < por default). O que importa é o servidor receber os
	// valores corretos depois do parse.
	var got sendRequest
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("unmarshal capturedBody: %v\nbody=%s", err, string(capturedBody))
	}
	if got.Channel != "email" {
		t.Errorf("channel %q, want email", got.Channel)
	}
	if got.To.Email != "user@example.com" {
		t.Errorf("to.email %q", got.To.Email)
	}
	if got.Subject != "Test subject" {
		t.Errorf("subject %q", got.Subject)
	}
	if got.HTMLBody != "<p>HTML</p>" {
		t.Errorf("html_body decodificada %q", got.HTMLBody)
	}
	if got.TextBody != "Plain" {
		t.Errorf("text_body %q", got.TextBody)
	}
	if got.Priority != "normal" {
		t.Errorf("priority %q", got.Priority)
	}
	// Template precisa estar omitempty=true — não pode aparecer "template":"".
	if strings.Contains(string(capturedBody), `"template":""`) {
		t.Error(`template não deveria aparecer vazio (omitempty)`)
	}
	if got.Template != "" {
		t.Errorf("template deveria ser vazio em raw mode, got %q", got.Template)
	}
}

// TestSendTemplate_Shape valida o caminho moderno: só template+vars,
// nenhum subject/body raw.
func TestSendTemplate_Shape(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, FixtureSendResponse)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	err := c.SendTemplate(context.Background(), "buyer@example.com", "checkout_paid", map[string]string{
		"order_short_id": "ORD-12345",
		"plan_name":      "1000 followers",
	})
	if err != nil {
		t.Fatalf("SendTemplate: %v", err)
	}

	var got sendRequest
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("unmarshal capturedBody: %v", err)
	}
	if got.Channel != "email" {
		t.Errorf("channel %q, want email", got.Channel)
	}
	if got.Template != "checkout_paid" {
		t.Errorf("template %q, want checkout_paid", got.Template)
	}
	if got.To.Email != "buyer@example.com" {
		t.Errorf("to.email %q", got.To.Email)
	}
	if got.Vars["order_short_id"] != "ORD-12345" || got.Vars["plan_name"] != "1000 followers" {
		t.Errorf("vars perdidos: %v", got.Vars)
	}
	if got.Subject != "" || got.HTMLBody != "" || got.TextBody != "" {
		t.Errorf("template path não deveria ter raw fields: %+v", got)
	}
}

// TestSendTelegram_Shape valida o channel=telegram + telegram_handle no to.
func TestSendTelegram_Shape(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, FixtureSendResponse)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	err := c.SendTelegram(context.Background(), "@buyer", "checkout_paid", map[string]string{
		"order_short_id": "ORD-9",
		"plan_name":      "Test",
	})
	if err != nil {
		t.Fatalf("SendTelegram: %v", err)
	}

	var got sendRequest
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Channel != "telegram" {
		t.Errorf("channel %q, want telegram", got.Channel)
	}
	if got.To.TelegramHandle != "@buyer" {
		t.Errorf("to.telegram_handle %q", got.To.TelegramHandle)
	}
	if got.To.Email != "" {
		t.Errorf("email não deveria ser populada em telegram channel: %q", got.To.Email)
	}
}

// TestSend_FailedStatus propaga erro quando provider falhou. attempt_id
// vai no erro pra que o caller possa correlacionar com sender_outbox.
func TestSend_FailedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"failed","attempt_id":"att_X"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	err := c.Send(context.Background(), application.EmailMessage{To: "x@x", Subject: "s", HTMLBody: "h"})
	if err == nil {
		t.Fatal("esperava erro quando status=failed")
	}
	if !strings.Contains(err.Error(), "att_X") {
		t.Errorf("erro deveria conter attempt_id pra correlação: %v", err)
	}
}

// TestSend_404MapsToErrNotFound prova que 404 do microserviço vira sentinel
// ErrNotFound, permitindo errors.Is no caller. Espelha o fix do paymentsclient
// (round 20): sem o sentinel, o erro vinha como string "HTTP 404" opaca e
// callers não conseguiam diferenciar 404 (caminho/template inexistente) de
// 500/503 (sender caiu) — bug que mascara mau-funcionamento em prod.
func TestSend_404MapsToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"template not registered"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-secret")
	err := c.SendTemplate(context.Background(), "x@x", "missing_template", nil)
	if err == nil {
		t.Fatal("esperava erro em 404, recebeu nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("erro deveria casar com ErrNotFound via errors.Is, got: %v", err)
	}
	// Outros status NÃO devem casar — defense pra evitar mapeamento errado.
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv500.Close()
	c2 := New(srv500.URL, "test-secret")
	err2 := c2.SendTemplate(context.Background(), "x@x", "t", nil)
	if err2 == nil {
		t.Fatal("esperava erro em 500")
	}
	if errors.Is(err2, ErrNotFound) {
		t.Errorf("500 NÃO deveria casar com ErrNotFound, got: %v", err2)
	}
}

// TestSendResponseFixture_DecodesCleanly garante que a fixture compartilhada
// continua decodável pelo client. Se o server mudar shape, o teste em
// viralefy_sender também falha.
func TestSendResponseFixture_DecodesCleanly(t *testing.T) {
	var got sendResponse
	if err := json.Unmarshal([]byte(FixtureSendResponse), &got); err != nil {
		t.Fatalf("fixture inválida: %v", err)
	}
	if got.Status != "queued" {
		t.Errorf("status %q, want queued", got.Status)
	}
	if got.AttemptID == "" {
		t.Error("attempt_id vazio na fixture")
	}
}
