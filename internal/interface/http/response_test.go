package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// decodeErrorBody reads the response recorder's body as the errorBody struct.
// Centralizing the parse keeps individual tests focused on assertions.
func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestWriteError_PgUniqueViolation_Returns409(t *testing.T) {
	rec := httptest.NewRecorder()
	pgErr := &pgconn.PgError{
		Code:    "23505",
		Message: `duplicate key value violates unique constraint "plans_category_name_key"`,
	}

	writeError(rec, pgErr)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	body := decodeErrorBody(t, rec)
	if body.Error.Code != "CONFLICT" {
		t.Errorf("expected error.code CONFLICT, got %q", body.Error.Code)
	}
	if body.Error.Message != "resource already exists" {
		t.Errorf("expected sanitized message, got %q", body.Error.Message)
	}
	// Guard: the raw constraint name must never leak to the client.
	if got := body.Error.Message; got == pgErr.Message {
		t.Errorf("raw pg message leaked to client: %q", got)
	}
	if body.Error.TraceID == "" {
		t.Errorf("expected non-empty trace_id")
	}
	if body.Error.Details == nil {
		t.Errorf("expected details to be an empty slice, got nil")
	}
}

func TestWriteError_OtherPgError_FallsThroughToSwitch(t *testing.T) {
	rec := httptest.NewRecorder()
	// 23503 = foreign_key_violation — should NOT be intercepted by the 23505 branch.
	pgErr := &pgconn.PgError{Code: "23503", Message: "fk violation"}

	writeError(rec, pgErr)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 (default), got %d", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("expected error.code INTERNAL_ERROR, got %q", body.Error.Code)
	}
	if body.Error.Message != "internal server error" {
		t.Errorf("expected default message, got %q", body.Error.Message)
	}
}

func TestWriteError_DomainNotFound_StillReturns404(t *testing.T) {
	rec := httptest.NewRecorder()

	writeError(rec, domain.ErrNotFound)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("expected error.code NOT_FOUND, got %q", body.Error.Code)
	}
	if body.Error.TraceID == "" {
		t.Errorf("expected non-empty trace_id")
	}
}

func TestWriteError_PgxErrNoRows_Returns404(t *testing.T) {
	// Round 20 simulated test descobriu que GET /v1/plans/{id}/payment-methods
	// retornava 500 quando o UUID era sintaticamente valido mas inexistente.
	// O repo retornava pgx.ErrNoRows que nao casava em domain.ErrNotFound.
	// Fix em writeError: detectar pgx.ErrNoRows como NOT_FOUND defensivamente.
	rec := httptest.NewRecorder()

	writeError(rec, pgx.ErrNoRows)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404 for pgx.ErrNoRows, got %d", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("expected error.code NOT_FOUND, got %q", body.Error.Code)
	}
	if body.Error.Message != "resource not found" {
		t.Errorf("expected sanitized message, got %q", body.Error.Message)
	}
	if body.Error.TraceID == "" {
		t.Errorf("expected non-empty trace_id")
	}
}

func TestWriteError_WrappedPgxErrNoRows_IsDetected(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapped := fmt.Errorf("repo: plan lookup: %w", pgx.ErrNoRows)

	writeError(rec, wrapped)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404 for wrapped pgx.ErrNoRows, got %d", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("expected error.code NOT_FOUND, got %q", body.Error.Code)
	}
}

func TestWriteError_WrappedPgError_IsDetected(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapped := fmt.Errorf("repo: %w", &pgconn.PgError{Code: "23505", Message: "dup"})

	writeError(rec, wrapped)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409 for wrapped pg error, got %d", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Error.Code != "CONFLICT" {
		t.Errorf("expected error.code CONFLICT, got %q", body.Error.Code)
	}
	if body.Error.Message != "resource already exists" {
		t.Errorf("expected sanitized message, got %q", body.Error.Message)
	}
}
