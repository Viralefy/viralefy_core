package domain

import (
	"context"
	"time"
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Instagram    string    `json:"instagram"`
	// Phone + Telegram — pelo menos UM é obrigatório no register (migration
	// 037). Canal de contato pós-pedido quando email cai em spam. Phone
	// aceita formato livre (E.164 não é forçado — suporte conversa antes
	// de cobrar formato pro user). Telegram aceita @handle ou link t.me/.
	Phone        string    `json:"phone,omitempty"`
	Telegram     string    `json:"telegram,omitempty"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	// TrackingData é o first-touch attribution (utm/fbclid/gclid/referrer/
	// landing_url + ip/user_agent server-side). Guardado uma vez no
	// register/checkout-anônimo; não atualizado depois.
	TrackingData map[string]any `json:"tracking_data,omitempty"`
}

// UserView é o user enriquecido com saldo (para listagens no admin).
type UserView struct {
	User
	BalanceCents int64 `json:"balance_cents"`
}

type UserRepository interface {
	Create(ctx context.Context, u User) error
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id string) (*User, error)
	ListWithCreditBalance(ctx context.Context, limit int) ([]UserView, error)
}
