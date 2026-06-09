package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// APIKeyRepo persiste credenciais B2B. Sempre opera com key_hash; nunca
// vê a key plain (essa responsabilidade fica no application service).
type APIKeyRepo struct{ db *DB }

func NewAPIKeyRepo(db *DB) *APIKeyRepo { return &APIKeyRepo{db: db} }

const apiKeyCols = `id, label, owner_user_id, revoked_at, created_at, last_used_at`

func scanAPIKey(row pgx.Row) (*domain.APIKey, error) {
	var k domain.APIKey
	var owner *string
	if err := row.Scan(
		&k.ID, &k.Label, &owner,
		&k.RevokedAt, &k.CreatedAt, &k.LastUsedAt,
	); err != nil {
		return nil, err
	}
	if owner != nil {
		k.OwnerUserID = *owner
	}
	return &k, nil
}

func (r *APIKeyRepo) Create(ctx context.Context, k domain.APIKey, keyHash string) error {
	if k.ID == "" || keyHash == "" {
		return domain.ErrInvalidInput
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO api_keys (id, key_hash, label, owner_user_id)
		VALUES ($1, $2, $3, $4)`,
		k.ID, keyHash, k.Label, k.OwnerUserID,
	)
	return err
}

// GetByHash devolve apenas keys ativas — o index parcial idx_api_keys_active
// cobre esse path. Keys revogadas batem ErrNotFound (rejeitadas pelo middleware).
func (r *APIKeyRepo) GetByHash(ctx context.Context, keyHash string) (*domain.APIKey, error) {
	row := r.db.pool.QueryRow(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys
		   WHERE key_hash = $1 AND revoked_at IS NULL`,
		keyHash,
	)
	k, err := scanAPIKey(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return k, err
}

func (r *APIKeyRepo) ListByUser(ctx context.Context, userID string) ([]domain.APIKey, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys
		   WHERE owner_user_id = $1
		   ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.APIKey, 0, 8)
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// Revoke marca revoked_at=NOW() apenas quando o caller é o dono. Não é
// destrutivo — preserva audit + UNIQUE(key_hash) impede reemissão acidental
// da mesma key (que aliás é impossível dado tamanho do espaço).
func (r *APIKeyRepo) Revoke(ctx context.Context, userID, keyID string) error {
	if userID == "" || keyID == "" {
		return domain.ErrInvalidInput
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE api_keys
		   SET revoked_at = NOW()
		 WHERE id = $1 AND owner_user_id = $2 AND revoked_at IS NULL`,
		keyID, userID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// MarkUsed atualiza last_used_at. Best-effort: caller usa em goroutine e
// ignora erro.
func (r *APIKeyRepo) MarkUsed(ctx context.Context, keyID string) error {
	_, err := r.db.pool.Exec(ctx,
		`UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`,
		keyID,
	)
	return err
}
