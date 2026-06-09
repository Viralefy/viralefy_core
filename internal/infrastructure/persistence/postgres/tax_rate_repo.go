package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// TaxRateRepo — leitor da tabela tax_rates (migration 027). Apenas leitura
// no MVP; updates de rate ficam por SQL manual ou tooling de backoffice
// posterior. ORDER BY country_code mantém o catálogo determinístico pra
// caching no front.
type TaxRateRepo struct {
	db *DB
}

func NewTaxRateRepo(db *DB) *TaxRateRepo {
	return &TaxRateRepo{db: db}
}

// GetByCountry normaliza o código (lower+trim) antes do lookup. Retorna
// ErrNotFound quando o país não está no catálogo — caller decide se
// trata como 0% (não EU/GB) ou erro. TaxService.ComputeTax escolhe 0%.
func (r *TaxRateRepo) GetByCountry(ctx context.Context, code string) (*domain.TaxRate, error) {
	row := r.db.pool.QueryRow(ctx,
		`SELECT country_code, rate_pct, rate_type FROM tax_rates WHERE country_code = $1`,
		strings.ToLower(strings.TrimSpace(code)),
	)
	var t domain.TaxRate
	if err := row.Scan(&t.CountryCode, &t.RatePct, &t.RateType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// List devolve o catálogo inteiro ordenado por country_code. Servido por
// GET /v1/tax-rates e cacheado no client (front baixa uma vez por sessão).
func (r *TaxRateRepo) List(ctx context.Context) ([]domain.TaxRate, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT country_code, rate_pct, rate_type FROM tax_rates ORDER BY country_code`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TaxRate{}
	for rows.Next() {
		var t domain.TaxRate
		if err := rows.Scan(&t.CountryCode, &t.RatePct, &t.RateType); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
