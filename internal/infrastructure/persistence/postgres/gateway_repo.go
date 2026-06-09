package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type GatewayRepo struct{ db *DB }

func NewGatewayRepo(db *DB) *GatewayRepo { return &GatewayRepo{db: db} }

// COALESCE garante linha antiga (pré-migration 032) não estourar com NULL no
// scan — o default da coluna só vale pra rows futuras.
const gatewaySelectCols = `id, name, provider, active, config,
	COALESCE(accepted_currencies, ARRAY[]::TEXT[]) AS accepted_currencies,
	created_at, updated_at`

func (r *GatewayRepo) ListAll(ctx context.Context) ([]domain.PaymentGateway, error) {
	rows, err := r.db.pool.Query(ctx, `
		SELECT `+gatewaySelectCols+` FROM payment_gateways ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []domain.PaymentGateway
	for rows.Next() {
		g, err := scanGateway(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *g)
	}
	return list, rows.Err()
}

func (r *GatewayRepo) GetByID(ctx context.Context, id string) (*domain.PaymentGateway, error) {
	row := r.db.pool.QueryRow(ctx, `
		SELECT `+gatewaySelectCols+` FROM payment_gateways WHERE id=$1`, id)
	g, err := scanGateway(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return g, err
}

func (r *GatewayRepo) GetDefaultActive(ctx context.Context) (*domain.PaymentGateway, error) {
	row := r.db.pool.QueryRow(ctx, `
		SELECT `+gatewaySelectCols+` FROM payment_gateways WHERE active=true ORDER BY created_at LIMIT 1`)
	g, err := scanGateway(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return g, err
}

func (r *GatewayRepo) GetActiveByProvider(ctx context.Context, provider string) (*domain.PaymentGateway, error) {
	row := r.db.pool.QueryRow(ctx, `
		SELECT `+gatewaySelectCols+`
		FROM payment_gateways WHERE active=true AND provider=$1 ORDER BY created_at LIMIT 1`, provider)
	g, err := scanGateway(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return g, err
}

func (r *GatewayRepo) Create(ctx context.Context, g domain.PaymentGateway) error {
	cfg, _ := json.Marshal(g.Config)
	ccy := g.AcceptedCurrencies
	if ccy == nil {
		ccy = []string{}
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO payment_gateways (id, name, provider, active, config, accepted_currencies)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		g.ID, g.Name, g.Provider, g.Active, cfg, ccy)
	return err
}

func (r *GatewayRepo) Update(ctx context.Context, g domain.PaymentGateway) error {
	cfg, _ := json.Marshal(g.Config)
	ccy := g.AcceptedCurrencies
	if ccy == nil {
		ccy = []string{}
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE payment_gateways
		SET name=$2, provider=$3, active=$4, config=$5, accepted_currencies=$6, updated_at=NOW()
		WHERE id=$1`,
		g.ID, g.Name, g.Provider, g.Active, cfg, ccy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *GatewayRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM payment_gateways WHERE id=$1`, id)
	if err != nil {
		// Postgres SQLSTATE 23503 = foreign_key_violation. Acontece quando
		// orders/invoices apontam pro gateway. UX correto = devolver Conflict
		// pra UI mostrar "tem N pedidos usando, desative em vez de apagar".
		// Hard delete só faz sentido pra gateways nunca usados; o resto deve
		// ser desativado via active=false.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return domain.ErrConflict
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func scanGateway(row pgx.Row) (*domain.PaymentGateway, error) {
	var g domain.PaymentGateway
	var raw []byte
	err := row.Scan(&g.ID, &g.Name, &g.Provider, &g.Active, &raw, &g.AcceptedCurrencies, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	g.Config = map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &g.Config)
	}
	if g.AcceptedCurrencies == nil {
		g.AcceptedCurrencies = []string{}
	}
	return &g, nil
}
