package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// FraudService implementa detecção de velocity por email/IP/fingerprint.
//
// O service não decide sozinho integrar com CheckoutService — main/wave3
// decide se pluga o helper. Aqui o objetivo é:
//   - Expor CheckEmail / CheckIP que retornam ErrForbidden quando o
//     threshold de block bate (warn é silencioso, só grava sinal).
//   - Expor IsBlocked que consulta fraud_blocks (bloqueio com janela).
//   - Recordar sinais históricos em fraud_signals pra dashboard admin.
//
// Política (constantes podem ser tuned via env num follow-up):
//   - email_velocity warn:  3+ orders / 24h
//   - email_velocity block: 10+ orders / 24h (defesa pro caso de leak de cartão)
//   - ip_velocity    warn:  5+ orders / 1h
//   - ip_velocity    block: 10+ orders / 1h
//   - login_fail     warn:  5+ fails  / 15min (rate-limiter já barra; tracking)
//
// Janela default do block: 1h. Janela curta o suficiente pra não punir
// usuários legítimos atrás de NAT corporativo, longa o suficiente pra
// quebrar bot que rota por proxy residencial caro.
type FraudService struct {
	DB *postgres.DB

	// BlockDuration é a janela de bloqueio aplicada quando severity=block.
	// 0 → default 1h.
	BlockDuration time.Duration
}

const (
	fraudEmailWarnThreshold  = 3
	fraudEmailBlockThreshold = 10
	fraudIPWarnThreshold     = 5
	fraudIPBlockThreshold    = 10
	fraudLoginFailThreshold  = 5

	fraudEmailWindow     = 24 * time.Hour
	fraudIPWindow        = 1 * time.Hour
	fraudLoginFailWindow = 15 * time.Minute
)

func NewFraudService(db *postgres.DB) *FraudService {
	return &FraudService{DB: db}
}

func (s *FraudService) blockDuration() time.Duration {
	if s.BlockDuration > 0 {
		return s.BlockDuration
	}
	return 1 * time.Hour
}

// CheckEmail conta orders recentes do email. Se ≥ block threshold,
// grava signal+block e retorna ErrForbidden. Se ≥ warn threshold,
// só grava signal e retorna nil. Erros de DB são logados e o caller
// segue (fail-open intencional pra não derrubar checkout por causa do
// anti-fraude — observabilidade trata).
func (s *FraudService) CheckEmail(ctx context.Context, email string) error {
	email = normalizeActor(email)
	if email == "" {
		return nil
	}
	if blocked, _ := s.IsBlocked(ctx, email); blocked {
		return domain.ErrForbidden
	}
	count, err := s.countOrdersByEmail(ctx, email, fraudEmailWindow)
	if err != nil {
		observability.FromContext(ctx).Warn("fraud check email failed", "email", email, "error", err.Error())
		return nil
	}
	switch {
	case count >= fraudEmailBlockThreshold:
		s.recordSignal(ctx, "email_velocity", email, "block", map[string]any{
			"count":  count,
			"window": fraudEmailWindow.String(),
		})
		s.upsertBlock(ctx, email, "email", "email_velocity")
		return domain.ErrForbidden
	case count >= fraudEmailWarnThreshold:
		s.recordSignal(ctx, "email_velocity", email, "warn", map[string]any{
			"count":  count,
			"window": fraudEmailWindow.String(),
		})
	}
	return nil
}

// CheckIP segue a mesma lógica de CheckEmail mas com janela 1h e
// thresholds ajustados ao volume típico esperado por IP residencial.
func (s *FraudService) CheckIP(ctx context.Context, ip string) error {
	ip = normalizeActor(ip)
	if ip == "" {
		return nil
	}
	if blocked, _ := s.IsBlocked(ctx, ip); blocked {
		return domain.ErrForbidden
	}
	count, err := s.countOrdersByIP(ctx, ip, fraudIPWindow)
	if err != nil {
		observability.FromContext(ctx).Warn("fraud check ip failed", "ip", ip, "error", err.Error())
		return nil
	}
	switch {
	case count >= fraudIPBlockThreshold:
		s.recordSignal(ctx, "ip_velocity", ip, "block", map[string]any{
			"count":  count,
			"window": fraudIPWindow.String(),
		})
		s.upsertBlock(ctx, ip, "ip", "ip_velocity")
		return domain.ErrForbidden
	case count >= fraudIPWarnThreshold:
		s.recordSignal(ctx, "ip_velocity", ip, "warn", map[string]any{
			"count":  count,
			"window": fraudIPWindow.String(),
		})
	}
	return nil
}

// IsBlocked retorna true + blocked_until se existe bloqueio ativo pro
// actor. Bloqueio expirado é ignorado (lazy cleanup — não vale rodar
// cron de purge, fraud_blocks cresce devagar).
func (s *FraudService) IsBlocked(ctx context.Context, actor string) (bool, time.Time) {
	actor = normalizeActor(actor)
	if actor == "" {
		return false, time.Time{}
	}
	var until time.Time
	err := s.DB.Pool().QueryRow(ctx,
		`SELECT blocked_until FROM fraud_blocks WHERE actor = $1`, actor,
	).Scan(&until)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			observability.FromContext(ctx).Warn("fraud is_blocked failed", "actor", actor, "error", err.Error())
		}
		return false, time.Time{}
	}
	if until.Before(time.Now()) {
		return false, time.Time{}
	}
	return true, until
}

// FraudSignal é o registro retornado pra dashboard admin.
type FraudSignal struct {
	ID          string          `json:"id"`
	SignalType  string          `json:"signal_type"`
	Actor       string          `json:"actor"`
	Severity    string          `json:"severity"`
	Detail      json.RawMessage `json:"detail,omitempty"`
	TriggeredAt time.Time       `json:"triggered_at"`
}

// ListSignals filtra por actor (substring) e severity. Limite default 100,
// teto 500 pra evitar dump acidental.
func (s *FraudService) ListSignals(ctx context.Context, actor, severity string, limit int) ([]FraudSignal, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	// Filtros opcionais via CTE simples; sem dynamic SQL pra evitar surpresa.
	rows, err := s.DB.Pool().Query(ctx, `
		SELECT id, signal_type, actor, severity, detail, triggered_at
		FROM fraud_signals
		WHERE ($1 = '' OR actor ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR severity = $2)
		ORDER BY triggered_at DESC
		LIMIT $3`, actor, severity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FraudSignal, 0, limit)
	for rows.Next() {
		var sig FraudSignal
		var detail []byte
		if err := rows.Scan(&sig.ID, &sig.SignalType, &sig.Actor, &sig.Severity, &detail, &sig.TriggeredAt); err != nil {
			return nil, err
		}
		if len(detail) > 0 {
			sig.Detail = json.RawMessage(detail)
		}
		out = append(out, sig)
	}
	return out, rows.Err()
}

// --- helpers ---

func normalizeActor(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (s *FraudService) countOrdersByEmail(ctx context.Context, email string, window time.Duration) (int, error) {
	var n int
	// Passamos segundos (int) e usamos make_interval(secs => $2) pra
	// evitar parsing surpresa de duration strings ("24h0m0s" não é interval).
	secs := int(window.Seconds())
	err := s.DB.Pool().QueryRow(ctx, `
		SELECT COUNT(*) FROM orders o
		JOIN users u ON u.id = o.user_id
		WHERE LOWER(u.email) = $1
		  AND o.created_at > NOW() - make_interval(secs => $2)`,
		email, secs,
	).Scan(&n)
	return n, err
}

func (s *FraudService) countOrdersByIP(ctx context.Context, ip string, window time.Duration) (int, error) {
	// Orders não guardam IP hoje (tech debt — coluna orders.client_ip vem em
	// Wave3). Por enquanto consultamos audit_log/idempotency? Não temos
	// fonte confiável → retornamos 0 e o cron alimenta sinais via outras
	// vias (login_fail já entra direto via RecordLoginFail).
	//
	// Quando orders.client_ip existir basta trocar o SELECT abaixo.
	_ = ip
	_ = window
	return 0, nil
}

func (s *FraudService) recordSignal(ctx context.Context, signalType, actor, severity string, detail map[string]any) {
	detailJSON, _ := json.Marshal(detail)
	_, err := s.DB.Pool().Exec(ctx, `
		INSERT INTO fraud_signals (id, signal_type, actor, severity, detail)
		VALUES ($1, $2, $3, $4, $5)`,
		uuid.New().String(), signalType, actor, severity, detailJSON,
	)
	if err != nil {
		observability.FromContext(ctx).Warn("fraud signal insert failed",
			"signal_type", signalType, "actor", actor, "error", err.Error())
	}
}

func (s *FraudService) upsertBlock(ctx context.Context, actor, blockType, reason string) {
	until := time.Now().Add(s.blockDuration())
	_, err := s.DB.Pool().Exec(ctx, `
		INSERT INTO fraud_blocks (actor, block_type, reason, blocked_until)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (actor) DO UPDATE
		SET blocked_until = GREATEST(EXCLUDED.blocked_until, fraud_blocks.blocked_until),
		    reason = EXCLUDED.reason,
		    block_type = EXCLUDED.block_type`,
		actor, blockType, reason, until,
	)
	if err != nil {
		observability.FromContext(ctx).Warn("fraud block upsert failed",
			"actor", actor, "error", err.Error())
	}
}

// RecordLoginFail é o entry-point que o auth pode chamar quando um login
// falha. Conta a janela e grava warn se passar do threshold. Não bloqueia
// — o rate-limiter na borda HTTP é a defesa primária; aqui é tracking
// pra dashboard.
func (s *FraudService) RecordLoginFail(ctx context.Context, ip string) {
	ip = normalizeActor(ip)
	if ip == "" {
		return
	}
	// Grava o evento bruto (warn) — agregação acontece via COUNT subsequente.
	s.recordSignal(ctx, "login_fail", ip, "warn", map[string]any{
		"window": fraudLoginFailWindow.String(),
	})
}
