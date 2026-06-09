package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// VendorService — CRUD admin de vendors (multi-vendor Fase 7.4).
//
// Scope MVP: criação, listagem, atualização (toggle active, ajuste de share).
// Sem self-onboarding nem portal próprio — apenas suporte interno cadastra.
// Settlement split é v2.5.
type VendorService struct {
	repo domain.VendorRepository
}

func NewVendorService(repo domain.VendorRepository) *VendorService {
	return &VendorService{repo: repo}
}

// CreateVendorInput é o payload do admin no backoffice.
type CreateVendorInput struct {
	Name            string  `json:"name"`
	ContactEmail    string  `json:"contact_email"`
	RevenueSharePct float64 `json:"revenue_share_pct"`
	Active          bool    `json:"active"`
}

// UpdateVendorInput — campos opcionais. Quando nil/zero, mantém o valor atual.
// (RevenueSharePct usa ponteiro pra distinguir "não enviado" de "zerar".)
type UpdateVendorInput struct {
	Name            *string  `json:"name,omitempty"`
	ContactEmail    *string  `json:"contact_email,omitempty"`
	RevenueSharePct *float64 `json:"revenue_share_pct,omitempty"`
	Active          *bool    `json:"active,omitempty"`
}

func (s *VendorService) Create(ctx context.Context, in CreateVendorInput) (*domain.Vendor, error) {
	name := strings.TrimSpace(in.Name)
	email := strings.ToLower(strings.TrimSpace(in.ContactEmail))
	if name == "" || email == "" {
		return nil, domain.ErrInvalidInput
	}
	if in.RevenueSharePct < 0 || in.RevenueSharePct > 100 {
		return nil, domain.ErrVendorInvalidShare
	}
	v := domain.Vendor{
		ID:              uuid.New().String(),
		Name:            name,
		ContactEmail:    email,
		RevenueSharePct: in.RevenueSharePct,
		Active:          in.Active,
	}
	if err := s.repo.Create(ctx, v); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, v.ID)
}

func (s *VendorService) Update(ctx context.Context, id string, in UpdateVendorInput) (*domain.Vendor, error) {
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, domain.ErrInvalidInput
		}
		current.Name = name
	}
	if in.ContactEmail != nil {
		email := strings.ToLower(strings.TrimSpace(*in.ContactEmail))
		if email == "" {
			return nil, domain.ErrInvalidInput
		}
		current.ContactEmail = email
	}
	if in.RevenueSharePct != nil {
		if *in.RevenueSharePct < 0 || *in.RevenueSharePct > 100 {
			return nil, domain.ErrVendorInvalidShare
		}
		current.RevenueSharePct = *in.RevenueSharePct
	}
	if in.Active != nil {
		current.Active = *in.Active
	}
	if err := s.repo.Update(ctx, *current); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, id)
}

func (s *VendorService) List(ctx context.Context) ([]domain.Vendor, error) {
	return s.repo.List(ctx)
}

func (s *VendorService) Get(ctx context.Context, id string) (*domain.Vendor, error) {
	return s.repo.GetByID(ctx, id)
}
