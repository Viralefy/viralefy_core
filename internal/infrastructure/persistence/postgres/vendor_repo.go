package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type VendorRepo struct{ db *DB }

func NewVendorRepo(db *DB) *VendorRepo { return &VendorRepo{db: db} }

const vendorCols = `id, name, contact_email, revenue_share_pct, active, created_at, updated_at`

func scanVendor(row pgx.Row) (*domain.Vendor, error) {
	var v domain.Vendor
	if err := row.Scan(&v.ID, &v.Name, &v.ContactEmail, &v.RevenueSharePct, &v.Active, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *VendorRepo) Create(ctx context.Context, v domain.Vendor) error {
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO vendors (id, name, contact_email, revenue_share_pct, active)
		VALUES ($1, $2, $3, $4, $5)`,
		v.ID, v.Name, strings.ToLower(strings.TrimSpace(v.ContactEmail)), v.RevenueSharePct, v.Active,
	)
	if err != nil {
		// UNIQUE violation no contact_email — devolve erro tipado pra UX.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrVendorEmailTaken
		}
		return err
	}
	return nil
}

func (r *VendorRepo) GetByID(ctx context.Context, id string) (*domain.Vendor, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+vendorCols+` FROM vendors WHERE id = $1`, id)
	v, err := scanVendor(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrVendorNotFound
	}
	return v, err
}

func (r *VendorRepo) List(ctx context.Context) ([]domain.Vendor, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+vendorCols+` FROM vendors ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Vendor{}
	for rows.Next() {
		v, err := scanVendor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (r *VendorRepo) Update(ctx context.Context, v domain.Vendor) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE vendors SET
			name = $2,
			contact_email = $3,
			revenue_share_pct = $4,
			active = $5,
			updated_at = NOW()
		WHERE id = $1`,
		v.ID, v.Name, strings.ToLower(strings.TrimSpace(v.ContactEmail)), v.RevenueSharePct, v.Active,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrVendorEmailTaken
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrVendorNotFound
	}
	return nil
}
