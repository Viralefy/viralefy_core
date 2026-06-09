package domain

import (
	"context"
	"time"
)

type InvoiceStatus string

const (
	InvoiceStatusPending   InvoiceStatus = "pending"
	InvoiceStatusPaid      InvoiceStatus = "paid"
	InvoiceStatusFailed    InvoiceStatus = "failed"
	InvoiceStatusCancelled InvoiceStatus = "cancelled"
)

// Invoice é a cobrança de recarga de créditos. Quando paga, gera um
// credit_transactions(type=recharge) e bate na credit_accounts.balance.
type Invoice struct {
	ID                 string            `json:"id"`
	UserID             string            `json:"user_id"`
	AmountCents        int64             `json:"amount_cents"`
	Currency           string            `json:"currency"`
	DisplayCurrency    string            `json:"display_currency"`
	DisplayAmount      string            `json:"display_amount"`
	SettlementCurrency string            `json:"settlement_currency"`
	SettlementAmount   string            `json:"settlement_amount"`
	Status             InvoiceStatus     `json:"status"`
	GatewayID          *string           `json:"gateway_id,omitempty"`
	ExternalRef        *string           `json:"external_ref,omitempty"`
	PaymentURL         *string           `json:"payment_url,omitempty"`
	PaymentExtra       map[string]string `json:"payment_extra,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	PaidAt             *time.Time        `json:"paid_at,omitempty"`
}

// InvoiceView é Invoice + dados do usuário hidratados via JOIN. Usado
// pelo backoffice em /v1/admin/invoices pra exibir nome/email do comprador
// sem um round-trip extra de GetByID.
type InvoiceView struct {
	Invoice
	UserName  string `json:"user_name,omitempty"`
	UserEmail string `json:"user_email,omitempty"`
}

type InvoiceRepository interface {
	Create(ctx context.Context, inv Invoice) error
	GetByID(ctx context.Context, id string) (*Invoice, error)
	GetByExternalRef(ctx context.Context, externalRef string) (*Invoice, error)
	ListByUser(ctx context.Context, userID string) ([]Invoice, error)
	ListAll(ctx context.Context, statusFilter string) ([]Invoice, error)
	ListAllView(ctx context.Context, statusFilter string) ([]InvoiceView, error)
	UpdatePayment(ctx context.Context, id, externalRef, paymentURL string, extra map[string]string) error
	MarkPaid(ctx context.Context, id string) error
	UpdateStatus(ctx context.Context, id string, status InvoiceStatus) error
}
