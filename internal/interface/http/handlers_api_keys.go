package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// Handlers de API keys (Fase 7.5 — B2B scaffold).
//
// Métodos são receivers de *Handlers (consistente com o resto), mas
// dependem do campo Handlers.APIKeys (adicionado via handlers_field_addition
// que o main loop integra). Quando o campo é nil (config sem service),
// devolve 503 — handler defensivo.
//
// Endpoints:
//   GET    /v1/me/api-keys           → MeListAPIKeys
//   POST   /v1/me/api-keys           → MeCreateAPIKey   (mostra key plain UMA vez)
//   DELETE /v1/me/api-keys/{id}      → MeRevokeAPIKey
//   GET    /v2/plans                 → PublicV2Plans     (X-API-Key)
//   GET    /v2/orders/{id}/status    → PublicV2OrderStatus (X-API-Key)

// MeListAPIKeys devolve as keys do usuário logado (metadados — nunca o plain).
func (h *Handlers) MeListAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.APIKeys == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	list, err := h.APIKeys.ListMyKeys(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// MeCreateAPIKey gera uma nova key. Resposta inclui {api_key, key} — o
// campo "key" é o plain e SÓ aparece nesse response. Front exibe em modal
// com warning ("save now — you won't see it again").
func (h *Handlers) MeCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.APIKeys == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	res, err := h.APIKeys.Create(r.Context(), userID, body.Label)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, res)
}

// MeRevokeAPIKey marca a key como revogada. Idempotente: revogar uma key
// já revogada retorna 404 (não distinguimos do "não existe").
func (h *Handlers) MeRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.APIKeys == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	if err := h.APIKeys.RevokeKey(r.Context(), userID, chi.URLParam(r, "id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- /v2 público (X-API-Key) --- //

// PublicV2Plans é o GET /v2/plans para parceiros B2B. Mesmo payload do
// /v1/plans (planos ativos), sem aggregateRating (não precisa pra B2B).
func (h *Handlers) PublicV2Plans(w http.ResponseWriter, r *http.Request) {
	if h.Plans == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	plans, err := h.Plans.ListPublic(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, plans)
}

// V2OrderStatus é a resposta enxuta de GET /v2/orders/{id}/status — só
// campos relevantes pra integrações B2B (status + identificadores).
// Evita vazar tracking/baseline/delivery/custom_data internos.
type V2OrderStatus struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	PlanID             string  `json:"plan_id"`
	AmountCents        int     `json:"amount_cents"`
	Currency           string  `json:"currency"`
	SettlementCurrency string  `json:"settlement_currency"`
	SettlementAmount   string  `json:"settlement_amount"`
	ExternalRef        *string `json:"external_ref,omitempty"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

// PublicV2OrderStatus devolve o status de um pedido. Cliente B2B precisa
// passar o id exato — não há listagem (multi-tenant fica pra v2.5).
func (h *Handlers) PublicV2OrderStatus(w http.ResponseWriter, r *http.Request) {
	if h.Orders == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	o, err := h.Orders.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if o == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	resp := V2OrderStatus{
		ID:                 o.ID,
		Status:             string(o.Status),
		PlanID:             o.PlanID,
		AmountCents:        o.AmountCents,
		Currency:           o.Currency,
		SettlementCurrency: o.SettlementCurrency,
		SettlementAmount:   o.SettlementAmount,
		ExternalRef:        o.ExternalRef,
		CreatedAt:          o.CreatedAt.Format(timeRFC3339Nano),
		UpdatedAt:          o.UpdatedAt.Format(timeRFC3339Nano),
	}
	writeData(w, http.StatusOK, resp)
}

// timeRFC3339Nano é o formato padrão da API. Evita import cíclico de time
// só pra ter o layout — declaramos como const.
const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
