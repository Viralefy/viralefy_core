package domain

import (
	"context"
	"time"
)

// CreditTransactionType — tipos do ledger imutável.
type CreditTransactionType string

const (
	CreditTxRecharge   CreditTransactionType = "recharge"   // entrada — recarga paga via invoice
	CreditTxSpend      CreditTransactionType = "spend"      // saída — pagou um pedido com créditos
	CreditTxRefund     CreditTransactionType = "refund"     // entrada — estorno
	CreditTxAdjustment CreditTransactionType = "adjustment" // entrada ou saída — ajuste manual (admin)
)

// CreditAccount é o saldo agregado por usuário (cache + invariante = SUM(credit_transactions)).
type CreditAccount struct {
	UserID       string    `json:"user_id"`
	BalanceCents int64     `json:"balance_cents"`
	Currency     string    `json:"currency"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CreditTransaction é uma entrada/saída no ledger. Imutável (somente INSERT).
type CreditTransaction struct {
	ID                string                `json:"id"`
	UserID            string                `json:"user_id"`
	Type              CreditTransactionType `json:"type"`
	AmountCents       int64                 `json:"amount_cents"`        // positivo = entrada, negativo = saída
	BalanceAfterCents int64                 `json:"balance_after_cents"` // snapshot para auditoria
	Currency          string                `json:"currency"`
	OrderID           *string               `json:"order_id,omitempty"`
	InvoiceID         *string               `json:"invoice_id,omitempty"`
	Description       string                `json:"description"`
	Metadata          map[string]string     `json:"metadata,omitempty"`
	CreatedAt         time.Time             `json:"created_at"`
}

type CreditRepository interface {
	// GetOrCreateAccount devolve a conta do usuário (cria com saldo 0 se não existir).
	GetOrCreateAccount(ctx context.Context, userID string) (*CreditAccount, error)
	GetAccount(ctx context.Context, userID string) (*CreditAccount, error)

	// Apply executa uma transação atômica: insere no ledger e atualiza balance_cents
	// na conta. Recebe o delta com sinal. Retorna o novo saldo.
	Apply(ctx context.Context, tx CreditTransaction) (*CreditAccount, error)

	ListByUser(ctx context.Context, userID string, limit int) ([]CreditTransaction, error)
}
