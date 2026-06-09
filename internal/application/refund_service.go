package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// RefundType — destino do estorno.
//
//   - to_credits — devolve em saldo (caminho 100% automatizado, sempre OK).
//   - to_gateway — solicita estorno no gateway externo. Hoje fica como
//     placeholder: gravamos a intenção + external_ref opcional, mas o
//     side-effect real depende do provider expor a API. Cron/admin manual
//     resolve a reconciliação. Log warn explícito pra ninguém esquecer.
type RefundType string

const (
	RefundTypeToCredits RefundType = "to_credits"
	RefundTypeToGateway RefundType = "to_gateway"
)

// RefundInput é o payload do AdminIssueRefund.
//
// AdminID identifica quem disparou (refunded_by, FK pra admins). OrderID
// + RefundUSDCents + RefundType são obrigatórios. Reason opcional pra
// log + auditoria. ExternalRef é preenchido pelo service quando o caminho
// for to_gateway e tivermos resposta do provider — input só usado se
// admin conseguir colar a ref manualmente (caso de provider que estornou
// pelo dashboard deles).
type RefundInput struct {
	OrderID        string
	AdminID        string
	RefundUSDCents int
	RefundType     RefundType
	Reason         string
	ExternalRef    string
}

// OrderRefund é uma linha de order_refunds — imutável (somente INSERT).
type OrderRefund struct {
	ID             string     `json:"id"`
	OrderID        string     `json:"order_id"`
	RefundUSDCents int        `json:"refund_usd_cents"`
	RefundType     RefundType `json:"refund_type"`
	Reason         string     `json:"reason,omitempty"`
	RefundedBy     string     `json:"refunded_by"`
	ExternalRef    string     `json:"external_ref,omitempty"`
	CreatedAt      string     `json:"created_at"`
}

// RefundService implementa IssueRefund + ListByOrder em cima do postgres.DB
// (pequeno escopo — não justifica repo dedicado). Depende do CreditService
// pra entrada no ledger quando refund_type=to_credits.
type RefundService struct {
	db      *postgres.DB
	credits *CreditService
}

func NewRefundService(db *postgres.DB, credits *CreditService) *RefundService {
	return &RefundService{db: db, credits: credits}
}

// IssueRefund valida invariantes e grava o estorno atomicamente.
//
//  1. Order existe + status='paid'.
//  2. RefundUSDCents > 0.
//  3. RefundUSDCents <= amount_cents - refunded_usd_cents (acumulado).
//  4. Se to_credits, credita o user via creditSvc.AdminAdjustment com
//     description identificando o order. Se to_gateway, warn no log.
//  5. INSERT em order_refunds + UPDATE em orders.refunded_usd_cents.
//
// Retorna o registro persistido.
func (s *RefundService) IssueRefund(ctx context.Context, in RefundInput) (*OrderRefund, error) {
	if s == nil || s.db == nil {
		return nil, domain.ErrInvalidInput
	}
	if in.OrderID == "" || in.AdminID == "" {
		return nil, domain.ErrInvalidInput
	}
	if in.RefundUSDCents <= 0 {
		return nil, domain.ErrInvalidInput
	}
	if in.RefundType != RefundTypeToCredits && in.RefundType != RefundTypeToGateway {
		return nil, domain.ErrInvalidInput
	}

	logger := observability.FromContext(ctx)

	// Lê estado atual do pedido. Não usa OrderRepository pra pegar
	// refunded_usd_cents direto (campo novo da migration 025; ainda não
	// está modelado no domain.Order). Mantemos o cálculo local até o
	// próximo refactor do repo.
	var (
		status          string
		amountCents     int
		userID          string
		refundedAlready int
	)
	err := s.db.Pool().QueryRow(ctx,
		`SELECT status, amount_cents, user_id, COALESCE(refunded_usd_cents,0)
		   FROM orders WHERE id=$1`,
		in.OrderID,
	).Scan(&status, &amountCents, &userID, &refundedAlready)
	if err != nil {
		return nil, domain.ErrNotFound
	}
	if status != string(domain.OrderStatusPaid) {
		return nil, fmt.Errorf("%w: order not paid", domain.ErrInvalidInput)
	}
	remaining := amountCents - refundedAlready
	if in.RefundUSDCents > remaining {
		return nil, fmt.Errorf("%w: refund exceeds remaining (%d)", domain.ErrInvalidInput, remaining)
	}

	// Side-effect financeiro antes de gravar a linha — se a parte de
	// crédito falhar não queremos uma row órfã em order_refunds. Pra
	// to_gateway é só log: caminho ainda manual.
	if in.RefundType == RefundTypeToCredits {
		if s.credits == nil {
			return nil, fmt.Errorf("%w: credits unavailable", domain.ErrInvalidInput)
		}
		if userID == "" {
			return nil, fmt.Errorf("%w: order without user, cannot credit", domain.ErrInvalidInput)
		}
		desc := fmt.Sprintf("Refund order %s", in.OrderID)
		if in.Reason != "" {
			desc = desc + " — " + in.Reason
		}
		if _, err := s.credits.AdminAdjustment(ctx, userID, int64(in.RefundUSDCents), desc); err != nil {
			return nil, err
		}
	} else {
		// to_gateway: placeholder. Provider integration pendente.
		logger.Warn("refund: to_gateway requested — implementar quando provider suportar",
			"order_id", in.OrderID,
			"amount_cents", in.RefundUSDCents,
			"admin_id", in.AdminID,
		)
	}

	// INSERT + UPDATE numa única transação pra manter a invariante
	// SUM(order_refunds) == orders.refunded_usd_cents.
	id := uuid.New().String()
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var reason *string
	if in.Reason != "" {
		r := in.Reason
		reason = &r
	}
	var extRef *string
	if in.ExternalRef != "" {
		e := in.ExternalRef
		extRef = &e
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO order_refunds (id, order_id, refund_usd_cents, refund_type, reason, refunded_by, external_ref)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, in.OrderID, in.RefundUSDCents, string(in.RefundType), reason, in.AdminID, extRef,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET refunded_usd_cents = COALESCE(refunded_usd_cents,0) + $2 WHERE id=$1`,
		in.OrderID, in.RefundUSDCents,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	out := &OrderRefund{
		ID:             id,
		OrderID:        in.OrderID,
		RefundUSDCents: in.RefundUSDCents,
		RefundType:     in.RefundType,
		Reason:         in.Reason,
		RefundedBy:     in.AdminID,
		ExternalRef:    in.ExternalRef,
	}
	return out, nil
}

// ListByOrder devolve todos os refunds de um pedido em ordem cronológica
// (mais antigo primeiro). Usado pelo backoffice pra exibir histórico no
// detalhe da order.
func (s *RefundService) ListByOrder(ctx context.Context, orderID string) ([]OrderRefund, error) {
	if s == nil || s.db == nil {
		return nil, domain.ErrInvalidInput
	}
	if orderID == "" {
		return nil, domain.ErrInvalidInput
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT id, order_id, refund_usd_cents, refund_type,
		        COALESCE(reason,''), refunded_by, COALESCE(external_ref,''),
		        to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')
		   FROM order_refunds WHERE order_id=$1 ORDER BY created_at ASC`,
		orderID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrderRefund{}
	for rows.Next() {
		var r OrderRefund
		var rt string
		if err := rows.Scan(&r.ID, &r.OrderID, &r.RefundUSDCents, &rt,
			&r.Reason, &r.RefundedBy, &r.ExternalRef, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		r.RefundType = RefundType(rt)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
