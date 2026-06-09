package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// RequirePermission é o gate RBAC por rota. Composição esperada:
// AdminAuth → injeta Principal no ctx → RequirePermission("plans:write")
// decide se passa ou bloqueia. Sem Principal no ctx, retorna 401.

func TestRequirePermission_Returns401WhenNoPrincipal(t *testing.T) {
	// Sem AdminAuth antes — RequirePermission só pode aceitar requests onde
	// o ctx já tem Principal. Caso contrário, é unauthorized.
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := RequirePermission("plans:write")(next)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if called {
		t.Errorf("next handler should NOT be called when principal missing")
	}
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRequirePermission_Returns403WhenPermissionMissing(t *testing.T) {
	// Principal existe mas não tem a permissão exata exigida.
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	handler := RequirePermission("plans:write")(next)

	p := domain.Principal{
		AdminID:     "admin-1",
		Role:        "support",
		Permissions: []string{"tickets:read", "users:read"}, // sem plans:write
	}
	ctx := context.WithValue(context.Background(), principalKey, p)
	r := httptest.NewRequest("PUT", "/v1/admin/plans/abc", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if called {
		t.Errorf("next handler should NOT be called")
	}
	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}

	var body errorBody
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body.Error.Code != "FORBIDDEN" {
		t.Errorf("error.code = %q, want FORBIDDEN", body.Error.Code)
	}
}

func TestRequirePermission_AllowsWhenPermissionGranted(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := RequirePermission("plans:write")(next)

	p := domain.Principal{
		AdminID:     "admin-2",
		Role:        "catalog_manager",
		Permissions: []string{"plans:write", "plans:read"},
	}
	ctx := context.WithValue(context.Background(), principalKey, p)
	r := httptest.NewRequest("PUT", "/v1/admin/plans/abc", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Errorf("next handler should be called when permission granted")
	}
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequirePermission_SuperadminBypassesAnyPermission(t *testing.T) {
	// Superadmin tem bypass — Can() retorna true mesmo sem entry específica.
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := RequirePermission("anything:everything")(next)

	p := domain.Principal{
		AdminID:     "admin-root",
		Role:        domain.RoleSuperadmin,
		Permissions: nil, // sem permissões explícitas
	}
	ctx := context.WithValue(context.Background(), principalKey, p)
	r := httptest.NewRequest("DELETE", "/v1/admin/dangerous", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Errorf("superadmin should bypass permission check")
	}
}

func TestRequirePermission_DifferentPermissionsAreIsolated(t *testing.T) {
	// Mesmo principal tem tickets:read mas não plans:write — gate por
	// permissão diferente deve responder diferente.
	p := domain.Principal{
		AdminID:     "admin-3",
		Role:        "support",
		Permissions: []string{"tickets:read"},
	}
	ctx := context.WithValue(context.Background(), principalKey, p)

	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// tickets:read → 200
	rA := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	wA := httptest.NewRecorder()
	RequirePermission("tickets:read")(noop).ServeHTTP(wA, rA)
	if wA.Code != 200 {
		t.Errorf("tickets:read = %d, want 200", wA.Code)
	}

	// plans:write → 403
	rB := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	wB := httptest.NewRecorder()
	RequirePermission("plans:write")(noop).ServeHTTP(wB, rB)
	if wB.Code != 403 {
		t.Errorf("plans:write = %d, want 403", wB.Code)
	}
}

// Principal injection round-trip — garante que principalKey e userIDKey
// não colidem (ambos são ctxKey "principal"/"user_id" — typed string).

func TestContextKeys_DoNotCollide(t *testing.T) {
	ctx := context.Background()
	ctx = context.WithValue(ctx, userIDKey, "user-1")
	ctx = context.WithValue(ctx, principalKey, domain.Principal{AdminID: "admin-1"})

	if userIDFromContext(ctx) != "user-1" {
		t.Errorf("userIDFromContext lost value")
	}
	p, ok := principalFromContext(ctx)
	if !ok || p.AdminID != "admin-1" {
		t.Errorf("principalFromContext lost value: %+v", p)
	}
}
