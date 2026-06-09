package application

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLookupSession_Paid valida o caminho feliz: 200 + payment_status=paid.
func TestLookupSession_Paid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stripe usa Basic auth com secret como user, password vazio.
		if u, _, ok := r.BasicAuth(); !ok || u != "sk_test_dummy" {
			t.Errorf("basic auth missing/wrong: %q", u)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
			t.Errorf("URL path errado: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"cs_test_123",
			"status":"complete",
			"payment_status":"paid"
		}`)
	}))
	defer srv.Close()

	c := &StripeReconcileCron{httpClient: &http.Client{}}
	// Stub a base URL: rewrite via custom roundtripper? Mais simples: copiar a
	// função lookupSession e validar diretamente com URL custom.
	// Como o helper hardcoda api.stripe.com, redirecionamos via roundtripper.
	c.httpClient.Transport = redirectTo{base: srv.URL}

	paid, status, err := c.lookupSession(context.Background(), "sk_test_dummy", "cs_test_123")
	if err != nil {
		t.Fatalf("lookupSession: %v", err)
	}
	if !paid {
		t.Error("esperava paid=true")
	}
	if status != "complete" {
		t.Errorf("status=%q, want complete", status)
	}
}

// TestLookupSession_Unpaid: provider responde mas payment_status=unpaid →
// não confunde com paid.
func TestLookupSession_Unpaid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cs_test","status":"open","payment_status":"unpaid"}`)
	}))
	defer srv.Close()

	c := &StripeReconcileCron{httpClient: &http.Client{Transport: redirectTo{base: srv.URL}}}
	paid, _, err := c.lookupSession(context.Background(), "sk_test_x", "cs_test")
	if err != nil {
		t.Fatalf("lookupSession: %v", err)
	}
	if paid {
		t.Error("esperava paid=false pra unpaid status")
	}
}

// TestLookupSession_404 propaga HTTP 404 (session expirada) sem panic.
func TestLookupSession_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"no_such_session"}`)
	}))
	defer srv.Close()

	c := &StripeReconcileCron{httpClient: &http.Client{Transport: redirectTo{base: srv.URL}}}
	_, _, err := c.lookupSession(context.Background(), "sk_test_x", "cs_gone")
	if err == nil {
		t.Fatal("esperava erro pra 404")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("erro deveria mencionar HTTP 404, got: %v", err)
	}
}

// TestLookupSession_429 retorna erro com HTTP 429 pra o caller pausar.
func TestLookupSession_429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
	}))
	defer srv.Close()

	c := &StripeReconcileCron{httpClient: &http.Client{Transport: redirectTo{base: srv.URL}}}
	_, _, err := c.lookupSession(context.Background(), "sk_test_x", "cs_throttled")
	if err == nil {
		t.Fatal("esperava erro pra 429")
	}
	if !strings.Contains(err.Error(), "HTTP 429") {
		t.Errorf("erro deveria conter HTTP 429 (tick usa pra pausar), got: %v", err)
	}
}

// TestLookupSession_EmptyArgs rejeita input vazio.
func TestLookupSession_EmptyArgs(t *testing.T) {
	c := &StripeReconcileCron{httpClient: &http.Client{}}
	_, _, err := c.lookupSession(context.Background(), "", "cs_test")
	if err == nil {
		t.Error("esperava erro pra secret vazio")
	}
	_, _, err = c.lookupSession(context.Background(), "sk_test_x", "")
	if err == nil {
		t.Error("esperava erro pra session_id vazio")
	}
}

// redirectTo é um RoundTripper que sobrescreve o host pra api.stripe.com →
// httptest.Server.URL, mantendo o path original. Usado nos testes pra não
// patch o package var hardcoded da URL.
type redirectTo struct{ base string }

func (r redirectTo) RoundTrip(req *http.Request) (*http.Response, error) {
	target := r.base + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	nreq, err := http.NewRequestWithContext(req.Context(), req.Method, target, req.Body)
	if err != nil {
		return nil, err
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			nreq.Header.Add(k, v)
		}
	}
	return http.DefaultTransport.RoundTrip(nreq)
}
