package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type ctxKey string

const principalKey ctxKey = "principal"
const userIDKey ctxKey = "user_id"

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", false
	}
	return strings.TrimPrefix(h, "Bearer "), true
}

// AdminAuth valida o token de admin e injeta o Principal (RBAC) no contexto.
func AdminAuth(auth *application.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				writeError(w, domain.ErrUnauthorized)
				return
			}
			principal, err := auth.ValidateAdmin(r.Context(), token)
			if err != nil {
				writeError(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequirePermission é o gate RBAC por rota. Deve ser usado após AdminAuth.
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := principalFromContext(r.Context())
			if !ok {
				writeError(w, domain.ErrUnauthorized)
				return
			}
			if !p.Can(perm) {
				writeError(w, domain.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireSuperadmin é mais estrito que RequirePermission — só passa se o
// principal for role=superadmin. Usado em operações DESTRUTIVAS (HARD
// delete de orders/invoices/users) que apagam a row do DB. Soft delete
// permanece em RequirePermission(PermAdminsManage) — admin comum só
// flagga, mas a row segue intacta pro superadmin auditar.
func RequireSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalFromContext(r.Context())
		if !ok {
			writeError(w, domain.ErrUnauthorized)
			return
		}
		if p.Role != domain.RoleSuperadmin {
			writeError(w, domain.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func principalFromContext(ctx context.Context) (domain.Principal, bool) {
	p, ok := ctx.Value(principalKey).(domain.Principal)
	return p, ok
}

// UserAuth valida o token de usuário (loja) e injeta o user_id no contexto.
func UserAuth(auth *application.UserAuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				writeError(w, domain.ErrUnauthorized)
				return
			}
			id, err := auth.ValidateToken(token)
			if err != nil {
				writeError(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func userIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey).(string)
	return id
}

// OptionalUserAuth: igual ao UserAuth mas não rejeita se não houver token.
// Quando há token válido, injeta o user_id; senão segue como request anônima.
// Útil em endpoints públicos que aceitam usuário logado opcionalmente
// (ex.: checkout — anônimo cria conta na hora, logado usa a existente + créditos).
func OptionalUserAuth(auth *application.UserAuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token, ok := bearerToken(r); ok {
				if id, err := auth.ValidateToken(token); err == nil {
					r = r.WithContext(context.WithValue(r.Context(), userIDKey, id))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
