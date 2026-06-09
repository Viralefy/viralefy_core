package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// Idempotency carrega o registro pra retry seguro de mutations.
//
// Cliente manda `Idempotency-Key: <uuid>` em POST/PATCH. Servidor:
//   1. Computa hash do body. Procura por (key).
//      - Hit + hash igual → devolve resposta cacheada (status + body).
//      - Hit + hash diferente → 409 (RFC draft idempotency-header §2.5).
//      - Miss → encaminha pra próxima handler, captura status+body, persiste,
//        devolve normal.
//   2. Records têm TTL de 24h via expires_at — limpos pelo
//      IdempotencyCleanupCron (application/idempotency_cleanup_cron.go).
//
// Sem header = bypass (middleware é opt-in via header).
func IdempotencyMiddleware(db *postgres.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Lê o body inteiro pra calcular hash e poder restaurar no request.
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewBuffer(body))

			h := sha256.Sum256(body)
			hash := hex.EncodeToString(h[:])

			row := db.Pool().QueryRow(r.Context(), `
				SELECT request_hash, response_status, response_body
				  FROM idempotency_keys WHERE key = $1 AND expires_at > NOW()`, key)
			var storedHash string
			var status int
			var stored []byte
			err := row.Scan(&storedHash, &status, &stored)

			if err == nil {
				if storedHash != hash {
					// Mesma key + corpo diferente — usuário mexeu nos dados.
					http.Error(w, `{"error":"idempotency_key_mismatch"}`, http.StatusConflict)
					return
				}
				// Replay perfeito.
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotent-Replay", "true")
				w.WriteHeader(status)
				_, _ = w.Write(stored)
				return
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				// Erro inesperado no DB — segue sem cache (fail-open).
				next.ServeHTTP(w, r)
				return
			}

			// Miss. Captura a resposta pra persistir.
			rec := &recordingWriter{ResponseWriter: w, body: &bytes.Buffer{}, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			// Persiste só status 2xx — erros são retentáveis sem cache.
			if rec.status >= 200 && rec.status < 300 {
				ctx, cancel := context.WithTimeout(r.Context(), 2*1e9) // 2s
				defer cancel()
				_, _ = db.Pool().Exec(ctx, `
					INSERT INTO idempotency_keys (key, method, path, request_hash,
					                              response_status, response_body)
					VALUES ($1,$2,$3,$4,$5,$6)
					ON CONFLICT (key) DO NOTHING`,
					key, r.Method, r.URL.Path, hash, rec.status, rec.body.Bytes())
			}
		})
	}
}

// recordingWriter intercepta o Write/WriteHeader pra capturar resposta.
type recordingWriter struct {
	http.ResponseWriter
	body   *bytes.Buffer
	status int
}

func (r *recordingWriter) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}

func (r *recordingWriter) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}
