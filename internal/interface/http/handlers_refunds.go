package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// AdminIssueRefund — POST /v1/admin/orders/{id}/refund
// Body: { refund_usd_cents, refund_type ("to_credits"|"to_gateway"), reason? }
//
// Política aplicada no service: order tem que estar paid, valor positivo e
// somatório (incluindo refunds anteriores) <= amount_cents. to_credits
// devolve no ledger via CreditService.AdminAdjustment; to_gateway é
// placeholder que grava intenção + log warn pra reconciliar manualmente.
func (h *Handlers) AdminIssueRefund(w http.ResponseWriter, r *http.Request) {
	if h.Refunds == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok || p.AdminID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	var body struct {
		RefundUSDCents int    `json:"refund_usd_cents"`
		RefundType     string `json:"refund_type"`
		Reason         string `json:"reason"`
		ExternalRef    string `json:"external_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	before, _ := h.Orders.GetByID(r.Context(), id)
	out, err := h.Refunds.IssueRefund(r.Context(), application.RefundInput{
		OrderID:        id,
		AdminID:        p.AdminID,
		RefundUSDCents: body.RefundUSDCents,
		RefundType:     application.RefundType(body.RefundType),
		Reason:         body.Reason,
		ExternalRef:    body.ExternalRef,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	after, _ := h.Orders.GetByID(r.Context(), id)
	h.logAudit(r, "refund", "order", id, before, after)
	writeData(w, http.StatusCreated, out)
}

// AdminListOrderRefunds — GET /v1/admin/orders/{id}/refunds
// Lista histórico de refunds emitidos sobre o pedido. Sem paginação:
// volume esperado por order é baixíssimo (0–2).
func (h *Handlers) AdminListOrderRefunds(w http.ResponseWriter, r *http.Request) {
	if h.Refunds == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	id := chi.URLParam(r, "id")
	list, err := h.Refunds.ListByOrder(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}
