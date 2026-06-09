package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// TwoFARepo implementa domain.TwoFARepository pra UMA tabela (admin_2fa OU
// user_2fa). A tabela é parametrizada no constructor — same SQL pra ambas.
type TwoFARepo struct {
	db    *DB
	table string // "admin_2fa" | "user_2fa"
	idCol string // "admin_id" | "user_id"
}

func NewAdminTwoFARepo(db *DB) *TwoFARepo {
	return &TwoFARepo{db: db, table: "admin_2fa", idCol: "admin_id"}
}

func NewUserTwoFARepo(db *DB) *TwoFARepo {
	return &TwoFARepo{db: db, table: "user_2fa", idCol: "user_id"}
}

func (r *TwoFARepo) Get(ctx context.Context, principalID string) (*domain.TwoFASecret, error) {
	row := r.db.pool.QueryRow(ctx,
		`SELECT `+r.idCol+`, secret_encrypted, backup_codes_hashed, enrolled_at, last_used_at
		   FROM `+r.table+` WHERE `+r.idCol+`=$1`, principalID)
	var s domain.TwoFASecret
	err := row.Scan(&s.PrincipalID, &s.SecretEncrypted, &s.BackupCodesHashed, &s.EnrolledAt, &s.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *TwoFARepo) Upsert(ctx context.Context, s domain.TwoFASecret) error {
	_, err := r.db.pool.Exec(ctx,
		`INSERT INTO `+r.table+` (`+r.idCol+`, secret_encrypted, backup_codes_hashed)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (`+r.idCol+`) DO UPDATE
		   SET secret_encrypted=EXCLUDED.secret_encrypted,
		       backup_codes_hashed=EXCLUDED.backup_codes_hashed,
		       enrolled_at=NULL,
		       last_used_at=NULL`,
		s.PrincipalID, s.SecretEncrypted, s.BackupCodesHashed)
	return err
}

func (r *TwoFARepo) MarkEnrolled(ctx context.Context, principalID string) error {
	tag, err := r.db.pool.Exec(ctx,
		`UPDATE `+r.table+` SET enrolled_at=NOW() WHERE `+r.idCol+`=$1`, principalID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *TwoFARepo) MarkUsed(ctx context.Context, principalID string) error {
	_, err := r.db.pool.Exec(ctx,
		`UPDATE `+r.table+` SET last_used_at=NOW() WHERE `+r.idCol+`=$1`, principalID)
	return err
}

// ConsumeBackupCode percorre os hashes, compara com bcrypt em constant-time,
// e remove o match. Sem match → retorna false (caller trata como invalid).
//
// Operação em transação: SELECT FOR UPDATE pra evitar TOCTOU (admin manda
// 2 requests com o mesmo backup code, ambas validam antes de remover).
func (r *TwoFARepo) ConsumeBackupCode(ctx context.Context, principalID, code string) (bool, error) {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var hashes []string
	err = tx.QueryRow(ctx,
		`SELECT backup_codes_hashed FROM `+r.table+` WHERE `+r.idCol+`=$1 FOR UPDATE`,
		principalID).Scan(&hashes)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	matchedIdx := -1
	for i, h := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(code)) == nil {
			matchedIdx = i
			break
		}
	}
	if matchedIdx < 0 {
		return false, nil
	}
	remaining := append([]string{}, hashes[:matchedIdx]...)
	remaining = append(remaining, hashes[matchedIdx+1:]...)
	if _, err := tx.Exec(ctx,
		`UPDATE `+r.table+` SET backup_codes_hashed=$2, last_used_at=NOW() WHERE `+r.idCol+`=$1`,
		principalID, remaining); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *TwoFARepo) Delete(ctx context.Context, principalID string) error {
	_, err := r.db.pool.Exec(ctx,
		`DELETE FROM `+r.table+` WHERE `+r.idCol+`=$1`, principalID)
	return err
}
