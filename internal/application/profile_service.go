package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type ProfileService struct {
	repo domain.ProfileRepository
}

func NewProfileService(repo domain.ProfileRepository) *ProfileService {
	return &ProfileService{repo: repo}
}

type AddProfileInput struct {
	UserID      string
	Platform    string
	Handle      string
	DisplayName string
}

func (s *ProfileService) List(ctx context.Context, userID string) ([]domain.Profile, error) {
	return s.repo.ListByUser(ctx, userID)
}

// GetByID expõe o repo pra handlers admin que hidratam relacionamentos
// (ex.: detalhe de pedido). User-facing devem usar GetForUser pra checar
// ownership.
func (s *ProfileService) GetByID(ctx context.Context, id string) (*domain.Profile, error) {
	return s.repo.GetByID(ctx, id)
}

// Add valida o handle e cria o perfil. Marca verified=true se passou no validador.
func (s *ProfileService) Add(ctx context.Context, in AddProfileInput) (*domain.Profile, error) {
	platform := domain.Platform(in.Platform)
	if err := ValidateHandle(platform, in.Handle); err != nil {
		return nil, domain.ErrInvalidInput
	}
	handle := NormalizeHandle(in.Handle)
	p := domain.Profile{
		ID:          uuid.New().String(),
		UserID:      in.UserID,
		Platform:    platform,
		Handle:      handle,
		DisplayName: in.DisplayName,
		Verified:    true, // passou no validador regex; verificação ao vivo seria etapa futura
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *ProfileService) Delete(ctx context.Context, id, userID string) error {
	return s.repo.Delete(ctx, id, userID)
}

// GetForUser garante ownership do perfil.
func (s *ProfileService) GetForUser(ctx context.Context, id, userID string) (*domain.Profile, error) {
	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.UserID != userID {
		return nil, domain.ErrForbidden
	}
	return p, nil
}
