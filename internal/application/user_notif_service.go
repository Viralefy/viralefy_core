package application

import (
	"context"
	"encoding/json"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// allowedNotifPrefKeys são as únicas chaves aceitas em users.notif_prefs.
// Qualquer chave fora dessa lista é rejeitada com ErrInvalidInput — evita
// que o front injete chaves arbitrárias no JSONB e vire schemaless drift.
var allowedNotifPrefKeys = map[string]struct{}{
	"order_updates":  {},
	"marketing":      {},
	"reviews":        {},
	"cart_recovery":  {},
}

// UserNotifService gerencia as preferências de notificação do usuário
// armazenadas em users.notif_prefs (JSONB). É um wrapper fino sobre o
// db porque o estado é pequeno e per-user — não justifica um repo
// dedicado.
type UserNotifService struct {
	db *postgres.DB
}

func NewUserNotifService(db *postgres.DB) *UserNotifService {
	return &UserNotifService{db: db}
}

// GetPrefs lê o JSONB do usuário e devolve um map de chaves bool.
// Se a coluna estiver vazia (caso raro pós-migration), o default é
// aplicado pelo Postgres — aqui só decodifica.
func (s *UserNotifService) GetPrefs(ctx context.Context, userID string) (map[string]bool, error) {
	if s == nil || s.db == nil {
		return nil, domain.ErrInvalidInput
	}
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	var raw []byte
	err := s.db.Pool().QueryRow(ctx, `SELECT notif_prefs FROM users WHERE id=$1`, userID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	// Garante que toda chave conhecida está presente — front pode renderizar
	// os 4 toggles sem precisar conhecer defaults.
	for k := range allowedNotifPrefKeys {
		if _, ok := out[k]; !ok {
			// defaults: order_updates/reviews/cart_recovery true, marketing false
			out[k] = k != "marketing"
		}
	}
	return out, nil
}

// UpdatePrefs valida que todas as chaves estão na allowlist e persiste.
// Chaves fora da allowlist → ErrInvalidInput, sem persistir nada.
// Chaves ausentes no input são preservadas (merge no Postgres via ||).
func (s *UserNotifService) UpdatePrefs(ctx context.Context, userID string, prefs map[string]bool) error {
	if s == nil || s.db == nil {
		return domain.ErrInvalidInput
	}
	if userID == "" {
		return domain.ErrUnauthorized
	}
	if prefs == nil {
		return domain.ErrInvalidInput
	}
	for k := range prefs {
		if _, ok := allowedNotifPrefKeys[k]; !ok {
			return domain.ErrInvalidInput
		}
	}
	payload, err := json.Marshal(prefs)
	if err != nil {
		return domain.ErrInvalidInput
	}
	// Merge via JSONB || pra preservar chaves não enviadas (forward-compat
	// caso o front mande só um subset).
	_, err = s.db.Pool().Exec(ctx,
		`UPDATE users SET notif_prefs = notif_prefs || $2::jsonb WHERE id=$1`,
		userID, string(payload),
	)
	return err
}
