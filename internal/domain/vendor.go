package domain

import (
	"context"
	"errors"
	"time"
)

// Vendor é um parceiro que pode registrar planos no catálogo da Viralefy.
//
// MVP (Fase 7.4): apenas data model + CRUD admin. Settlement split (computar
// parte do vendor por order paid e disparar payout) fica como roadmap v2.5.
//
// RevenueSharePct é o % do GMV que vai pro vendor (default 70). Armazenado
// como NUMERIC(5,2) — usamos float64 aqui pra mapear sem precisão decimal
// adicional; cálculo de settlement no futuro deve usar inteiros (basis points)
// pra evitar drift.
type Vendor struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	ContactEmail    string    `json:"contact_email"`
	RevenueSharePct float64   `json:"revenue_share_pct"`
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Errors específicos de vendor — sobem pra UX no backoffice.
var (
	ErrVendorNotFound       = errors.New("vendor not found")
	ErrVendorEmailTaken     = errors.New("vendor contact email already in use")
	ErrVendorInvalidShare   = errors.New("revenue share must be between 0 and 100")
)

// VendorRepository é a porta de saída para persistência. Mantemos minimal
// (Create/GetByID/List/Update) — Delete fica fora porque vendors aparecem
// em FK de plans; desativação é via Active=false.
type VendorRepository interface {
	Create(ctx context.Context, v Vendor) error
	GetByID(ctx context.Context, id string) (*Vendor, error)
	List(ctx context.Context) ([]Vendor, error)
	Update(ctx context.Context, v Vendor) error
}
