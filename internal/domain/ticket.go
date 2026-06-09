package domain

import (
	"context"
	"time"
)

type TicketStatus string

const (
	TicketStatusOpen     TicketStatus = "open"     // aguardando suporte
	TicketStatusPending  TicketStatus = "pending"  // aguardando cliente
	TicketStatusResolved TicketStatus = "resolved" // resolvido (cliente pode reabrir)
	TicketStatusClosed   TicketStatus = "closed"   // fechado, não reabre
)

type TicketPriority string

const (
	TicketPriorityLow    TicketPriority = "low"
	TicketPriorityNormal TicketPriority = "normal"
	TicketPriorityHigh   TicketPriority = "high"
	TicketPriorityUrgent TicketPriority = "urgent"
)

type TicketAuthorType string

const (
	TicketAuthorUser  TicketAuthorType = "user"
	TicketAuthorAdmin TicketAuthorType = "admin"
)

type Ticket struct {
	ID              string         `json:"id"`
	UserID          string         `json:"user_id"`
	Subject         string         `json:"subject"`
	Status          TicketStatus   `json:"status"`
	Priority        TicketPriority `json:"priority"`
	OrderID         *string        `json:"order_id,omitempty"`
	AssignedAdminID *string        `json:"assigned_admin_id,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// TicketView agrega o ticket com dados úteis para listagem (nome do usuário,
// quantidade de mensagens, autor da última mensagem).
type TicketView struct {
	Ticket
	UserName       string    `json:"user_name"`
	UserEmail      string    `json:"user_email"`
	MessageCount   int       `json:"message_count"`
	LastMessageAt  time.Time `json:"last_message_at"`
	LastAuthorType string    `json:"last_author_type"`
}

type TicketMessage struct {
	ID         string           `json:"id"`
	TicketID   string           `json:"ticket_id"`
	AuthorType TicketAuthorType `json:"author_type"`
	AuthorID   string           `json:"author_id"`
	AuthorName string           `json:"author_name"`
	Body       string           `json:"body"`
	CreatedAt  time.Time        `json:"created_at"`
}

type TicketRepository interface {
	Create(ctx context.Context, t Ticket) error
	GetByID(ctx context.Context, id string) (*Ticket, error)
	ListByUser(ctx context.Context, userID string) ([]Ticket, error)
	ListAllView(ctx context.Context, statusFilter string) ([]TicketView, error)
	GetView(ctx context.Context, id string) (*TicketView, error)
	UpdateStatus(ctx context.Context, id string, status TicketStatus) error
	UpdatePriority(ctx context.Context, id string, priority TicketPriority) error
	AssignAdmin(ctx context.Context, id string, adminID *string) error

	AppendMessage(ctx context.Context, m TicketMessage) error
	ListMessages(ctx context.Context, ticketID string) ([]TicketMessage, error)
}
