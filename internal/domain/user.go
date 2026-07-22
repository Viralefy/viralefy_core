package domain

import (
	"context"
	"time"
)

type User struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Instagram string `json:"instagram"`
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
	// Soft-delete (migration 020 + 045). Quando deleted_at != nil, o user
	// fica invisível pra UI da loja (login bloqueado em viralefy_auth,
	// listagens /v1/me/* vazias). Painel admin lista inclusive deletados
	// com badge — superadmin pode HARD-delete depois.
	DeletedAt        *time.Time `json:"deleted_at,omitempty"`
	DeletedByAdminID *string    `json:"deleted_by_admin_id,omitempty"`
	DeleteReason     *string    `json:"delete_reason,omitempty"`
}

// UserView é o user enriquecido com saldo (para listagens no admin).
type UserView struct {
	User
	BalanceCents int64 `json:"balance_cents"`
}

// UserListQuery é a janela + filtros de uma listagem admin de clientes.
//
// O quê: parâmetros já validados na borda HTTP, prontos pro repositório.
// Onde:  montada por AdminListUsers a partir da query string; consumida por
//
//	UserRepository.ListPageWithCreditBalance.
//
// Efeitos: nenhum — valor puro.
type UserListQuery struct {
	// Limit é sempre >= 1 (a borda garante).
	Limit int
	// CursorTime/CursorID = posição keyset da página anterior. Zero = 1ª página.
	CursorTime time.Time
	CursorID   string
	// Search casa por email OU nome, case-insensitive. Vazio = sem filtro.
	Search string
	// IncludeTest liga a exibição de fixtures `@viralefy.test`. Default false:
	// o backoffice lista CLIENTES, e persona de teste não é cliente.
	IncludeTest bool
}

type UserRepository interface {
	Create(ctx context.Context, u User) error
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id string) (*User, error)
	// ListPageWithCreditBalance devolve uma página + o total que casa com os
	// filtros (total ignora o cursor, senão a UI não sabe o tamanho real).
	ListPageWithCreditBalance(ctx context.Context, q UserListQuery) ([]UserView, int, error)
	SoftDeleteUser(ctx context.Context, id, adminID, reason string) error
	HardDeleteUser(ctx context.Context, id string) error
	RestoreUser(ctx context.Context, id string) error
	// ListDeletedWithCreditBalance pra aba Trash do superadmin.
	ListDeletedWithCreditBalance(ctx context.Context, limit int) ([]UserView, error)
}
