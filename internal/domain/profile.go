package domain

import (
	"context"
	"time"
)

type Platform string

const (
	PlatformInstagram Platform = "instagram"
	PlatformTikTok    Platform = "tiktok"
)

// IsValid evita serviço sendo enviado pra plataforma errada.
func (p Platform) IsValid() bool {
	return p == PlatformInstagram || p == PlatformTikTok
}

type TargetType string

const (
	TargetProfile     TargetType = "profile"     // serviço vai num perfil (seguidores, etc.)
	TargetPublication TargetType = "publication" // serviço vai numa publicação (curtidas em foto/vídeo)
)

func (t TargetType) IsValid() bool {
	return t == TargetProfile || t == TargetPublication
}

type Profile struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	Platform    Platform  `json:"platform"`
	Handle      string    `json:"handle"`       // sem @
	DisplayName string    `json:"display_name"` // apelido amigável (ex.: "Pessoal", "Marca")
	Verified    bool      `json:"verified"`     // passou no validador
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProfileRepository interface {
	Create(ctx context.Context, p Profile) error
	GetByID(ctx context.Context, id string) (*Profile, error)
	ListByUser(ctx context.Context, userID string) ([]Profile, error)
	ListByUserAndPlatform(ctx context.Context, userID string, platform Platform) ([]Profile, error)
	Delete(ctx context.Context, id string, userID string) error
	SetVerified(ctx context.Context, id string, verified bool) error
}
