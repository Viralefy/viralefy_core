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
//   - RequestDeletion: registra a intenção com status='pending' e
//     executes_at = NOW()+30d. O hard-delete físico é executado pelo
//     binário `cmd/user-deletion-cron` (systemd timer diário) —
//     resolveu o tech-debt original. Cron lê WHERE status='pending'
//     AND executes_at <= NOW().
//   - CancelDeletion: usuário desistiu DENTRO da grace window — marca
//     a request como cancelled. Após status='executed' não há cancel
//     possível (dados já foram apagados).
//   - GetDeletionStatus: introspecção pro frontend — retorna estado
//     corrente + tempo restante + categorias de dados afetadas.
//
// Mantemos UPSERT (UNIQUE user_id) pra simplificar o ciclo "pedi →
// cancelei → pedi de novo" sem proliferar linhas. UPSERT é gated por
// `WHERE status <> 'executed'`: se já foi executado, INSERT vai falhar
// na FK (users.id não existe mais) — comportamento desejado.
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
//
// Re-arma só rows com status ∈ {pending, cancelled, failed} — uma row
// `executed` é estado terminal (user já não existe) e nunca deveria
// colidir aqui de qualquer forma.
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
		   SET requested_at  = NOW(),
		       executes_at   = EXCLUDED.executes_at,
		       status        = 'pending',
		       reason        = EXCLUDED.reason,
		       executed_at   = NULL,
		       error_message = NULL
		 WHERE user_deletion_requests.status <> 'executed'
	`, uuid.New().String(), userID, executesAt, reason)
	return err
}

// CancelDeletion marca a request ativa como cancelled. Idempotente:
// chamar sem request pendente é no-op. Após status='executed' (hard
// delete já rodou) o user nem existe mais — caller normalmente nem
// chega aqui (401 antes), mas a query é segura por construção.
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

// DeletionStatus é o shape retornado pra UI consultar o estado do
// pedido. Strings sempre presentes; campos de tempo opcionais.
type DeletionStatus struct {
	// Status: "none" (nunca pediu), "pending" (na grace window),
	// "cancelled", "executed", "failed".
	Status string `json:"status"`
	// RequestedAt: quando o usuário pediu (RFC3339 UTC); vazio se none.
	RequestedAt string `json:"requested_at,omitempty"`
	// ExecutesAt: quando o cron vai apagar (RFC3339 UTC); vazio se none.
	ExecutesAt string `json:"executes_at,omitempty"`
	// SecondsRemaining: segundos até executes_at. 0 quando já passou
	// (cron vai pegar na próxima execução) ou estado terminal.
	SecondsRemaining int64 `json:"seconds_remaining"`
	// Reason informado pelo usuário (texto livre, opcional).
	Reason string `json:"reason,omitempty"`
	// DeletedCategories lista o que vai ser apagado hard quando o cron
	// rodar — transparência LGPD Art. 9 (informação clara).
	DeletedCategories []string `json:"deleted_categories"`
	// RetainedCategories lista o que NÃO vai ser apagado, com motivo
	// (retenção fiscal, audit imutável).
	RetainedCategories []string `json:"retained_categories"`
}

// GetDeletionStatus devolve o estado atual + transparência sobre o que
// é apagado/retido. NÃO falha se não houver request (status="none").
func (s *UserDataService) GetDeletionStatus(ctx context.Context, userID string) (*DeletionStatus, error) {
	if s == nil || s.db == nil {
		return nil, domain.ErrInvalidInput
	}
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}

	out := &DeletionStatus{
		Status:             "none",
		DeletedCategories:  dataCategoriesDeleted,
		RetainedCategories: dataCategoriesRetained,
	}

	row := s.db.Pool().QueryRow(ctx, `
		SELECT status, requested_at, executes_at, COALESCE(reason,'')
		  FROM user_deletion_requests
		 WHERE user_id = $1`, userID)
	var status, reason string
	var requestedAt, executesAt time.Time
	if err := row.Scan(&status, &requestedAt, &executesAt, &reason); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// pgx.ErrNoRows ou outras leituras vazias: retorna status="none"
		// sem propagar erro — a UI quer "você não pediu exclusão".
		return out, nil
	}
	out.Status = status
	out.RequestedAt = requestedAt.UTC().Format(time.RFC3339)
	out.ExecutesAt = executesAt.UTC().Format(time.RFC3339)
	out.Reason = reason
	if status == "pending" {
		remaining := time.Until(executesAt)
		if remaining < 0 {
			remaining = 0
		}
		out.SecondsRemaining = int64(remaining.Seconds())
	}
	return out, nil
}

// dataCategoriesDeleted lista o que o cron hard-deleta. Texto user-
// facing — passou por revisão de UX/LGPD. Mantém alinhado com a query
// do cron em cmd/user-deletion-cron/main.go.
var dataCategoriesDeleted = []string{
	"Conta e credenciais (e-mail, senha, telefone, telegram)",
	"Perfis sociais cadastrados",
	"Tokens de sessão e API keys",
	"Configurações de 2FA",
	"Assinaturas (subscriptions)",
	"Eventos de comportamento e jornada de uso",
	"Saldo de créditos e transações de crédito",
	"Tickets de atendimento e mensagens",
	"Reviews escritas",
	"Programa de indicação (referrals)",
	"Sinais de antifraude vinculados ao e-mail",
}

// dataCategoriesRetained lista o que permanece, com motivo. LGPD
// Art. 16: obrigação legal/regulatória supera direito ao apagamento
// quando o controlador tem dever de retenção (fiscal/contábil).
var dataCategoriesRetained = []string{
	"Pedidos (orders) — anonimizados (vinculação removida) por 5 anos para retenção fiscal",
	"Refunds e invoices — preservados por obrigação contábil",
	"Audit log — preservado com PII anonimizada (imutabilidade da trilha)",
}
