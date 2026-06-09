package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type CurrencyRepo struct{ db *DB }

func NewCurrencyRepo(db *DB) *CurrencyRepo { return &CurrencyRepo{db: db} }

const currencyCols = `code, name, symbol, rate, decimals, kind, display_enabled, settlement_code, sort_order`

func (r *CurrencyRepo) ListAll(ctx context.Context) ([]domain.Currency, error) {
	return r.query(ctx, `SELECT `+currencyCols+` FROM currencies ORDER BY sort_order ASC`)
}

func (r *CurrencyRepo) ListDisplayable(ctx context.Context) ([]domain.Currency, error) {
	return r.query(ctx, `SELECT `+currencyCols+` FROM currencies WHERE display_enabled = true ORDER BY sort_order ASC`)
}

func (r *CurrencyRepo) query(ctx context.Context, sql string, args ...any) ([]domain.Currency, error) {
	rows, err := r.db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.Currency{}
	for rows.Next() {
		c, err := scanCurrency(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *c)
	}
	return list, rows.Err()
}

func (r *CurrencyRepo) GetByCode(ctx context.Context, code string) (*domain.Currency, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+currencyCols+` FROM currencies WHERE code = $1`, code)
	c, err := scanCurrency(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return c, err
}

func (r *CurrencyRepo) UpdateRate(ctx context.Context, code string, rate float64, displayEnabled bool, settlementCode string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE currencies SET rate=$2, display_enabled=$3, settlement_code=$4, updated_at=NOW() WHERE code=$1`,
		code, rate, displayEnabled, settlementCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func scanCurrency(row pgx.Row) (*domain.Currency, error) {
	var c domain.Currency
	err := row.Scan(&c.Code, &c.Name, &c.Symbol, &c.Rate, &c.Decimals, &c.Kind, &c.DisplayEnabled, &c.SettlementCode, &c.SortOrder)
	return &c, err
}
