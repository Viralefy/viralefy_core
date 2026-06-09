package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type CreditRepo struct{ db *DB }

func NewCreditRepo(db *DB) *CreditRepo { return &CreditRepo{db: db} }

// GetOrCreateAccount: idempotente. Cria com saldo 0 se não existir.
func (r *CreditRepo) GetOrCreateAccount(ctx context.Context, userID string) (*domain.CreditAccount, error) {
	if _, err := r.db.pool.Exec(ctx, `
		INSERT INTO credit_accounts (user_id, balance_cents, currency)
		VALUES ($1, 0, 'BRL') ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return nil, err
	}
	return r.GetAccount(ctx, userID)
}

func (r *CreditRepo) GetAccount(ctx context.Context, userID string) (*domain.CreditAccount, error) {
	row := r.db.pool.QueryRow(ctx, `
		SELECT user_id, balance_cents, currency, updated_at FROM credit_accounts WHERE user_id=$1`, userID)
	var a domain.CreditAccount
	err := row.Scan(&a.UserID, &a.BalanceCents, &a.Currency, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return &a, err
}

// Apply executa atomicamente: lê saldo com FOR UPDATE, valida invariante
// (saldo não pode ficar negativo), insere no ledger e atualiza
// credit_accounts. Tudo numa única transação Postgres.
func (r *CreditRepo) Apply(ctx context.Context, txData domain.CreditTransaction) (*domain.CreditAccount, error) {
	// Garante existência da conta antes do FOR UPDATE.
	if _, err := r.db.pool.Exec(ctx, `
		INSERT INTO credit_accounts (user_id, balance_cents, currency) VALUES ($1, 0, 'BRL')
		ON CONFLICT (user_id) DO NOTHING`, txData.UserID); err != nil {
		return nil, err
	}

	dbtx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer dbtx.Rollback(ctx)

	var balance int64
	if err := dbtx.QueryRow(ctx,
		`SELECT balance_cents FROM credit_accounts WHERE user_id=$1 FOR UPDATE`, txData.UserID).Scan(&balance); err != nil {
		return nil, err
	}

	newBalance := balance + txData.AmountCents
	if newBalance < 0 {
		// Saldo insuficiente — chamador interpreta como 422.
		return nil, domain.ErrInvalidInput
	}
	txData.BalanceAfterCents = newBalance

	meta, _ := json.Marshal(txData.Metadata)
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	if _, err := dbtx.Exec(ctx, `
		INSERT INTO credit_transactions
		  (id, user_id, type, amount_cents, balance_after_cents, currency, order_id, invoice_id, description, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		txData.ID, txData.UserID, txData.Type, txData.AmountCents, txData.BalanceAfterCents, txData.Currency,
		txData.OrderID, txData.InvoiceID, txData.Description, meta); err != nil {
		return nil, err
	}

	if _, err := dbtx.Exec(ctx,
		`UPDATE credit_accounts SET balance_cents=$2, updated_at=NOW() WHERE user_id=$1`,
		txData.UserID, newBalance); err != nil {
		return nil, err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, err
	}
	return &domain.CreditAccount{
		UserID:       txData.UserID,
		BalanceCents: newBalance,
		Currency:     txData.Currency,
	}, nil
}

func (r *CreditRepo) ListByUser(ctx context.Context, userID string, limit int) ([]domain.CreditTransaction, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.pool.Query(ctx, `
		SELECT id, user_id, type, amount_cents, balance_after_cents, currency,
		       order_id, invoice_id, description, metadata, created_at
		FROM credit_transactions
		WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.CreditTransaction{}
	for rows.Next() {
		var t domain.CreditTransaction
		var meta []byte
		if err := rows.Scan(&t.ID, &t.UserID, &t.Type, &t.AmountCents, &t.BalanceAfterCents, &t.Currency,
			&t.OrderID, &t.InvoiceID, &t.Description, &meta, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Metadata = map[string]string{}
		if len(meta) > 0 {
			_ = json.Unmarshal(meta, &t.Metadata)
		}
		list = append(list, t)
	}
	return list, rows.Err()
}
