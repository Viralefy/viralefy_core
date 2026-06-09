package http

import (
	"context"
	"net/http"

	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// apiKeyHeader é o header padrão para credenciais B2B no /v2.
const apiKeyHeader = "X-API-Key"

// apiKeyOwnerKey é a chave de contexto onde o middleware injeta o
// owner_user_id da API key autenticada. Handlers /v2 podem ler via
// apiKeyOwnerFromContext caso precisem distinguir o tenant.
const apiKeyOwnerKey ctxKey = "api_key_owner"

// apiKeyIDKey guarda o id da API key autenticada (útil pra audit).
const apiKeyIDKey ctxKey = "api_key_id"

// apiKeyAuth é o middleware que protege /v2/*. Extrai X-API-Key, valida
// via APIKeyService e injeta owner_user_id no contexto. Falha sempre
// devolve 401 (sem distinguir "key inválida" de "key revogada").
func apiKeyAuth(svc *application.APIKeyService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plain := r.Header.Get(apiKeyHeader)
			if plain == "" {
				writeError(w, domain.ErrUnauthorized)
				return
			}
			k, err := svc.ValidateKey(r.Context(), plain)
			if err != nil {
				writeError(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), apiKeyOwnerKey, k.OwnerUserID)
			ctx = context.WithValue(ctx, apiKeyIDKey, k.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// apiKeyOwnerFromContext devolve o owner_user_id injetado pelo middleware.
// Vazio quando o request não veio por /v2 (ou middleware não rodou).
func apiKeyOwnerFromContext(ctx context.Context) string {
	id, _ := ctx.Value(apiKeyOwnerKey).(string)
	return id
}
