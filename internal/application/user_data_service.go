package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// UserDataService cobre o fluxo "Manage my data" (LGPD/GDPR):
//   - ExportData: snapshot de TUDO que o sistema tem do usuário,
//     em JSON serializável. Best-effort por sub-query: se uma tabela
//     opcional não existir (ex.: notification_preferences ainda não
//     migrada), logamos warn e seguimos — o usuário não fica refém
//     de uma tabela.
//   - RequestDeletion: registra a intenção. A execução física do
//     delete é tech debt (cron futuro). Janela de 30 dias pra cancelar.
//   - CancelDeletion: usuário desistiu — marca a request como cancelled.
//
// Mantemos UPSERT (UNIQUE user_id) pra simplificar o ciclo "pedi →
// cancelei → pedi de novo" sem proliferar linhas.
type UserDataService struct {
	db *postgres.DB
}

func NewUserDataService(db *postgres.DB) *UserDataService {
	return &UserDataService{db: db}
}

// deletionWindow é o tempo que o usuário tem pra mudar de ideia antes
// do hard-delete físico. 30 dias é o piso comum em CCPA/GDPR pra
// "right to be forgotten" honrando obrigações contábeis.
const deletionWindow = 30 * 24 * time.Hour

// ExportData devolve um dump JSON-friendly com tudo do usuário. Erros
// em sub-queries são logados como warning e a chave correspondente vira
// array vazio — o export NUNCA falha por causa de uma tabela opcional.
func (s *UserDataService) ExportData(ctx context.Context, userID string) (map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, domain.ErrInvalidInput
	}
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	logger := observability.FromContext(ctx)
	out := map[string]any{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"user_id":     userID,
	}

	// ---- user ----
	{
		row := s.db.Pool().QueryRow(ctx,
			`SELECT id, email, name, COALESCE(instagram,''), created_at,
			        COALESCE(deleted_at, 'epoch'::timestamptz) AS deleted_at_raw
			   FROM users WHERE id=$1`, userID)
		var id, email, name, ig string
		var createdAt, deletedAt time.Time
		if err := row.Scan(&id, &email, &name, &ig, &createdAt, &deletedAt); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			logger.Warn("user_data export: user lookup failed", "user_id", userID, "error", err.Error())
		} else {
			u := map[string]any{
				"id":         id,
				"email":      email,
				"name":       name,
				"instagram":  ig,
				"created_at": createdAt.UTC().Format(time.RFC3339),
			}
			// Mantém deleted_at fora do dump quando vazio (epoch).
			if !deletedAt.IsZero() && deletedAt.Year() > 1970 {
				u["deleted_at"] = deletedAt.UTC().Format(time.RFC3339)
			}
			out["user"] = u
		}
	}

	// ---- orders ----
	out["orders"] = s.collectRows(ctx, logger, "orders",
		`SELECT id, plan_id, status, amount_cents, currency,
		        COALESCE(external_ref,''), created_at, updated_at
		   FROM orders WHERE user_id=$1 ORDER BY created_at DESC`,
		userID,
		[]string{"id", "plan_id", "status", "amount_cents", "currency", "external_ref", "created_at", "updated_at"},
	)

	// ---- tickets ----
	out["tickets"] = s.collectRows(ctx, logger, "tickets",
		`SELECT id, subject, status, priority,
		        COALESCE(order_id,''), created_at, updated_at
		   FROM tickets WHERE user_id=$1 ORDER BY created_at DESC`,
		userID,
		[]string{"id", "subject", "status", "priority", "order_id", "created_at", "updated_at"},
	)

	// ---- profiles ----
	out["profiles"] = s.collectRows(ctx, logger, "profiles",
		`SELECT id, platform, handle, COALESCE(display_name,''),
		        verified, created_at
		   FROM profiles WHERE user_id=$1 ORDER BY created_at DESC`,
		userID,
		[]string{"id", "platform", "handle", "display_name", "verified", "created_at"},
	)

	// ---- reviews ----
	out["reviews"] = s.collectRows(ctx, logger, "reviews",
		`SELECT id, order_id, plan_id, rating,
		        COALESCE(title,''), COALESCE(body,''), visible, created_at
		   FROM reviews WHERE user_id=$1 ORDER BY created_at DESC`,
		userID,
		[]string{"id", "order_id", "plan_id", "rating", "title", "body", "visible", "created_at"},
	)

	// ---- notification preferences ----
	// notif_prefs vive como JSONB em users (migration 019). Tabela
	// dedicada (notification_preferences) ainda não existe — quando
	// migrarmos, basta swap aqui.
	{
		row := s.db.Pool().QueryRow(ctx,
			`SELECT COALESCE(notif_prefs::text, '{}') FROM users WHERE id=$1`, userID)
		var rawJSON string
		if err := row.Scan(&rawJSON); err != nil {
			logger.Warn("user_data export: notif_prefs lookup failed", "user_id", userID, "error", err.Error())
			out["notification_preferences"] = map[string]any{}
		} else {
			out["notification_preferences"] = rawJSON
		}
	}

	// ---- deletion request status (se houver) ----
	{
		row := s.db.Pool().QueryRow(ctx,
			`SELECT requested_at, executes_at, status, COALESCE(reason,'')
			   FROM user_deletion_requests WHERE user_id=$1`, userID)
		var requestedAt, executesAt time.Time
		var status, reason string
		if err := row.Scan(&requestedAt, &executesAt, &status, &reason); err == nil {
			out["deletion_request"] = map[string]any{
				"requested_at": requestedAt.UTC().Format(time.RFC3339),
				"executes_at":  executesAt.UTC().Format(time.RFC3339),
				"status":       status,
				"reason":       reason,
			}
		}
	}

	return out, nil
}

// collectRows é o helper best-effort: roda a query; se falhar (tabela
// não existe, coluna renomeada, etc.), loga warn e devolve []any vazio.
// Mantém shape estável da resposta — front pode iterar sem checar nil.
func (s *UserDataService) collectRows(
	ctx context.Context,
	logger interface {
		Warn(msg string, args ...any)
	},
	label, query string,
	userID string,
	cols []string,
) []map[string]any {
	rows, err := s.db.Pool().Query(ctx, query, userID)
	if err != nil {
		logger.Warn("user_data export: subquery failed", "table", label, "error", err.Error())
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		// Buffers genéricos por coluna — usamos any pra evitar duplicar
		// código por shape de tabela. pgx hidrata strings/ints/timestamps
		// no tipo apropriado e o json.Marshal cuida do resto.
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			logger.Warn("user_data export: row scan failed", "table", label, "error", err.Error())
			continue
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = normalizeExportValue(vals[i])
		}
		out = append(out, row)
	}
	return out
}

// normalizeExportValue converte time.Time → RFC3339 e []byte → string
// para o output JSON ficar legível (pgx pode devolver tipos não-óbvios
// pra colunas TEXT em alguns drivers). Resto passa direto.
func normalizeExportValue(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case []byte:
		return string(x)
	default:
		return v
	}
}

// RequestDeletion grava (ou re-arma) o pedido de exclusão. UPSERT pra
// suportar o ciclo "pedi → cancelei → pedi de novo" sem proliferar
// linhas (a UNIQUE em user_id garante uma única request ativa).
func (s *UserDataService) RequestDeletion(ctx context.Context, userID, reason string) error {
	if s == nil || s.db == nil {
		return domain.ErrInvalidInput
	}
	if userID == "" {
		return domain.ErrUnauthorized
	}
	executesAt := time.Now().UTC().Add(deletionWindow)
	_, err := s.db.Pool().Exec(ctx, `
		INSERT INTO user_deletion_requests (id, user_id, requested_at, executes_at, status, reason)
		VALUES ($1, $2, NOW(), $3, 'pending', $4)
		ON CONFLICT (user_id) DO UPDATE
		   SET requested_at = NOW(),
		       executes_at  = EXCLUDED.executes_at,
		       status       = 'pending',
		       reason       = EXCLUDED.reason
	`, uuid.New().String(), userID, executesAt, reason)
	return err
}

// CancelDeletion marca a request ativa como cancelled. Idempotente:
// chamar sem request pendente é no-op.
func (s *UserDataService) CancelDeletion(ctx context.Context, userID string) error {
	if s == nil || s.db == nil {
		return domain.ErrInvalidInput
	}
	if userID == "" {
		return domain.ErrUnauthorized
	}
	_, err := s.db.Pool().Exec(ctx, `
		UPDATE user_deletion_requests
		   SET status = 'cancelled'
		 WHERE user_id = $1 AND status = 'pending'
	`, userID)
	return err
}
