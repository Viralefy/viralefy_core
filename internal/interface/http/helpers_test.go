package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// ---------- clientIP ----------

func TestClientIP_PrefersXForwardedForFirstIP(t *testing.T) {
	// Caddy/Cloudflare prepend o IP do cliente original. Pega o primeiro,
	// ignora o resto da chain de proxies.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.42, 10.0.0.1, 10.0.0.2")
	if got := clientIP(r); got != "203.0.113.42" {
		t.Errorf("clientIP = %q, want 203.0.113.42", got)
	}
}

func TestClientIP_HandlesSingleXForwardedFor(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := clientIP(r); got != "198.51.100.7" {
		t.Errorf("clientIP = %q, want 198.51.100.7", got)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.50:54321"
	if got := clientIP(r); got != "192.0.2.50" {
		t.Errorf("clientIP = %q, want 192.0.2.50 (port stripped)", got)
	}
}

func TestClientIP_EmptyWhenNothingAvailable(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = ""
	if got := clientIP(r); got != "" {
		t.Errorf("clientIP = %q, want empty", got)
	}
}

func TestClientIP_StripsLeadingWhitespaceInXFF(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "  203.0.113.42  ,10.0.0.1")
	if got := clientIP(r); got != "203.0.113.42" {
		t.Errorf("clientIP = %q, want trimmed 203.0.113.42", got)
	}
}

// ---------- bearerToken ----------

func TestBearerToken_ExtractsValidToken(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer abc.def.ghi")
	tok, ok := bearerToken(r)
	if !ok || tok != "abc.def.ghi" {
		t.Errorf("bearerToken = (%q, %v), want (abc.def.ghi, true)", tok, ok)
	}
}

func TestBearerToken_RejectsMissingHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	_, ok := bearerToken(r)
	if ok {
		t.Errorf("bearerToken should fail on missing header")
	}
}

func TestBearerToken_RejectsWrongScheme(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	_, ok := bearerToken(r)
	if ok {
		t.Errorf("bearerToken should reject Basic auth")
	}
}

// ---------- writeError ----------

func TestWriteError_ErrNotFound_404(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, domain.ErrNotFound)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
	var body errorBody
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("error.code = %q, want NOT_FOUND", body.Error.Code)
	}
	if body.Error.TraceID == "" {
		t.Errorf("trace_id must be populated")
	}
}

func TestWriteError_ErrInvalidInput_422(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, domain.ErrInvalidInput)
	if w.Code != 422 {
		t.Errorf("status = %d, want 422 (Unprocessable Entity)", w.Code)
	}
	var body errorBody
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body.Error.Code != "INVALID_INPUT" {
		t.Errorf("error.code = %q, want INVALID_INPUT", body.Error.Code)
	}
}

func TestWriteError_ErrUnauthorized_401(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, domain.ErrUnauthorized)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
	var body errorBody
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body.Error.Code != "UNAUTHORIZED" {
		t.Errorf("error.code = %q, want UNAUTHORIZED", body.Error.Code)
	}
}

func TestWriteError_ErrForbidden_403(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, domain.ErrForbidden)
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestWriteError_ErrConflict_409(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, domain.ErrConflict)
	if w.Code != 409 {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestWriteError_UnknownErrorReturns500(t *testing.T) {
	// Erros que não batem em domain.Err* devem cair em 500 + INTERNAL_ERROR.
	w := httptest.NewRecorder()
	writeError(w, errors.New("boom"))
	if w.Code != 500 {
		t.Errorf("status = %d, want 500 fallback", w.Code)
	}
	var body errorBody
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("error.code = %q, want INTERNAL_ERROR", body.Error.Code)
	}
	// Mensagem genérica — NUNCA vazar err.Error() pra cliente em 500.
	if strings.Contains(body.Error.Message, "boom") {
		t.Errorf("error.message leaks internal error: %q", body.Error.Message)
	}
}

func TestWriteError_ErrorBodyShapeIsContractStable(t *testing.T) {
	// O front depende do envelope {"error":{"code","message","trace_id","details"}}.
	// Qualquer mudança aqui é breaking change pro cliente.
	w := httptest.NewRecorder()
	writeError(w, domain.ErrNotFound)
	var raw map[string]any
	_ = json.NewDecoder(w.Body).Decode(&raw)
	errObj, ok := raw["error"].(map[string]any)
	if !ok {
		t.Fatalf("response must wrap in {error:{...}}")
	}
	for _, key := range []string{"code", "message", "trace_id", "details"} {
		if _, ok := errObj[key]; !ok {
			t.Errorf("error envelope missing key %q", key)
		}
	}
}

// ---------- writeData ----------

func TestWriteData_WrapsInDataEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	writeData(w, 201, map[string]any{"id": "abc"})
	if w.Code != 201 {
		t.Errorf("status = %d, want 201", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	_ = json.NewDecoder(w.Body).Decode(&body)
	data, ok := body["data"].(map[string]any)
	if !ok || data["id"] != "abc" {
		t.Errorf("body = %v, want {data:{id:abc}}", body)
	}
}

// ---------- context helpers ----------

func TestUserIDFromContext_ReturnsEmptyWhenAbsent(t *testing.T) {
	if got := userIDFromContext(context.Background()); got != "" {
		t.Errorf("userIDFromContext on empty ctx = %q, want empty", got)
	}
}

func TestUserIDFromContext_ReturnsInjectedValue(t *testing.T) {
	ctx := context.WithValue(context.Background(), userIDKey, "user-abc")
	if got := userIDFromContext(ctx); got != "user-abc" {
		t.Errorf("userIDFromContext = %q, want user-abc", got)
	}
}

func TestPrincipalFromContext_ReturnsFalseWhenAbsent(t *testing.T) {
	_, ok := principalFromContext(context.Background())
	if ok {
		t.Errorf("principalFromContext should return false on empty ctx")
	}
}

func TestPrincipalFromContext_ReturnsInjectedPrincipal(t *testing.T) {
	want := domain.Principal{AdminID: "a-1", Role: "support", Permissions: []string{"tickets:read"}}
	ctx := context.WithValue(context.Background(), principalKey, want)
	got, ok := principalFromContext(ctx)
	if !ok {
		t.Fatalf("principalFromContext returned false")
	}
	if got.AdminID != want.AdminID || got.Role != want.Role {
		t.Errorf("principal = %+v, want %+v", got, want)
	}
}
