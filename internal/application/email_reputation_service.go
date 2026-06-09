package application

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// EmailReputationService recebe eventos de webhook (Resend) e atualiza
// email_reputation. Auto-disable em hard_bounce ou complaint.
//
// Não temos repository abstrato porque o pacote application já depende
// do postgres.DB em outros lugares (DeliveryCaptureCron, IdempotencyCleanup).
// Mover pra repo formal se algum dia trocarmos backend.
type EmailReputationService struct {
	DB *postgres.DB
}

func NewEmailReputationService(db *postgres.DB) *EmailReputationService {
	return &EmailReputationService{DB: db}
}

// ResendEvent é o envelope que o webhook do Resend manda.
// Estrutura simplificada — só os campos que importam.
type ResendEvent struct {
	Type string `json:"type"` // "email.delivered" | "email.bounced" | "email.complained" | etc.
	Data struct {
		To           []string `json:"to"`
		Email        string   `json:"email"`
		BounceType   string   `json:"bounce_type"` // "hard" | "soft" (presente em email.bounced)
		BounceReason string   `json:"bounce_reason"`
	} `json:"data"`
}

// RecordResendEvent grava o evento e atualiza o sumário rolante. Idempotente
// pra retries do Resend — events insert é sempre nova row, reputation usa
// UPSERT incremental.
func (s *EmailReputationService) RecordResendEvent(ctx context.Context, raw []byte) error {
	var ev ResendEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return err
	}
	email := pickEmail(ev)
	if email == "" {
		return nil // sem destinatário identificável — nada pra correlacionar
	}
	email = strings.ToLower(strings.TrimSpace(email))

	eventType := mapResendType(ev.Type)
	if eventType == "" {
		return nil // tipo não monitorado (opened/clicked também passam, mas ignoramos)
	}

	id := uuid.New().String()
	if _, err := s.DB.Pool().Exec(ctx, `
		INSERT INTO email_events (id, email, event_type, bounce_type, payload)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5::jsonb)`,
		id, email, eventType, ev.Data.BounceType, string(raw),
	); err != nil {
		return err
	}

	// Atualiza sumário rolante + disabled flag conforme política.
	switch eventType {
	case "delivered":
		_, err := s.DB.Pool().Exec(ctx, `
			INSERT INTO email_reputation (email, delivered_count, last_event_at, updated_at)
			VALUES ($1, 1, NOW(), NOW())
			ON CONFLICT (email) DO UPDATE SET
				delivered_count = email_reputation.delivered_count + 1,
				last_event_at = NOW(),
				updated_at = NOW()`, email)
		return err
	case "bounced":
		if ev.Data.BounceType == "hard" {
			_, err := s.DB.Pool().Exec(ctx, `
				INSERT INTO email_reputation (
					email, disabled, disabled_reason, disabled_at,
					hard_bounce_count, last_event_at, updated_at
				) VALUES ($1, TRUE, 'hard_bounce', NOW(), 1, NOW(), NOW())
				ON CONFLICT (email) DO UPDATE SET
					disabled = TRUE,
					disabled_reason = COALESCE(email_reputation.disabled_reason, 'hard_bounce'),
					disabled_at = COALESCE(email_reputation.disabled_at, NOW()),
					hard_bounce_count = email_reputation.hard_bounce_count + 1,
					last_event_at = NOW(),
					updated_at = NOW()`, email)
			return err
		}
		_, err := s.DB.Pool().Exec(ctx, `
			INSERT INTO email_reputation (email, soft_bounce_count, last_event_at, updated_at)
			VALUES ($1, 1, NOW(), NOW())
			ON CONFLICT (email) DO UPDATE SET
				soft_bounce_count = email_reputation.soft_bounce_count + 1,
				last_event_at = NOW(),
				updated_at = NOW()`, email)
		return err
	case "complained":
		_, err := s.DB.Pool().Exec(ctx, `
			INSERT INTO email_reputation (
				email, disabled, disabled_reason, disabled_at,
				complaint_count, last_event_at, updated_at
			) VALUES ($1, TRUE, 'complaint', NOW(), 1, NOW(), NOW())
			ON CONFLICT (email) DO UPDATE SET
				disabled = TRUE,
				disabled_reason = COALESCE(email_reputation.disabled_reason, 'complaint'),
				disabled_at = COALESCE(email_reputation.disabled_at, NOW()),
				complaint_count = email_reputation.complaint_count + 1,
				last_event_at = NOW(),
				updated_at = NOW()`, email)
		return err
	}
	return nil
}

// IsDisabled retorna true se o endereço está marcado pra não receber.
// Usado pelo email sender pra short-circuit antes de chamar a API do Resend.
func (s *EmailReputationService) IsDisabled(ctx context.Context, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var disabled bool
	err := s.DB.Pool().QueryRow(ctx, `
		SELECT disabled FROM email_reputation WHERE email = $1`, email,
	).Scan(&disabled)
	if err != nil {
		// "no rows" significa nunca visto — não disabled.
		if strings.Contains(err.Error(), "no rows") {
			return false, nil
		}
		return false, err
	}
	return disabled, nil
}

func pickEmail(ev ResendEvent) string {
	if ev.Data.Email != "" {
		return ev.Data.Email
	}
	if len(ev.Data.To) > 0 {
		return ev.Data.To[0]
	}
	return ""
}

func mapResendType(t string) string {
	switch t {
	case "email.delivered":
		return "delivered"
	case "email.bounced":
		return "bounced"
	case "email.complained":
		return "complained"
	}
	return ""
}
