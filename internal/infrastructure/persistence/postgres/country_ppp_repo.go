package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type CountryPPPRepo struct {
	db *DB
}

func NewCountryPPPRepo(db *DB) *CountryPPPRepo {
	return &CountryPPPRepo{db: db}
}

// GetByCode normaliza o código (lower) antes do lookup. Retorna ErrNotFound
// quando o país não está no catálogo PPP — caller decide se trata como 1.00.
func (r *CountryPPPRepo) GetByCode(ctx context.Context, code string) (*domain.CountryPPP, error) {
	row := r.db.pool.QueryRow(ctx,
		`SELECT country_code, multiplier FROM country_ppp WHERE country_code = $1`,
		strings.ToLower(strings.TrimSpace(code)),
	)
	var p domain.CountryPPP
	if err := row.Scan(&p.Code, &p.Multiplier); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// List devolve o catálogo inteiro ordenado por country_code. Pequeno (<50
// linhas) — front baixa uma vez por sessão e mantém em memória.
func (r *CountryPPPRepo) List(ctx context.Context) ([]domain.CountryPPP, error) {
	rows, err := r.db.pool.Query(ctx,
		`SELECT country_code, multiplier FROM country_ppp ORDER BY country_code`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CountryPPP{}
	for rows.Next() {
		var p domain.CountryPPP
		if err := rows.Scan(&p.Code, &p.Multiplier); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
