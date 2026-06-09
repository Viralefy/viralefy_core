package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// MeCreateReview — POST /v1/me/reviews
// Body: { order_id, rating: 1..5, title, body, country_code }
// Auth: userAuth. ReviewService valida ownership + status=paid.
func (h *Handlers) MeCreateReview(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	var body struct {
		OrderID     string `json:"order_id"`
		Rating      int    `json:"rating"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		CountryCode string `json:"country_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	rev, err := h.Reviews.Create(r.Context(), application.CreateReviewInput{
		UserID:      userID,
		OrderID:     strings.TrimSpace(body.OrderID),
		Rating:      body.Rating,
		Title:       body.Title,
		Body:        body.Body,
		CountryCode: body.CountryCode,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, rev)
}

// MeGetReviewForOrder — GET /v1/me/reviews/by-order/{order_id}
// Devolve o review existente ou 404. A página /orders/{id}/review usa
// pra decidir se mostra o form ou o estado "thanks, already submitted".
func (h *Handlers) MeGetReviewForOrder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	orderID := chi.URLParam(r, "order_id")
	rev, err := h.Reviews.GetByOrder(r.Context(), orderID)
	if err != nil {
		writeError(w, err)
		return
	}
	if rev == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	// Ownership: só devolve o review pro dono do order. Não vaza pra outros
	// users mesmo logados.
	order, err := h.Orders.GetByID(r.Context(), orderID)
	if err != nil {
		writeError(w, err)
		return
	}
	if order.UserID != userID {
		writeError(w, domain.ErrForbidden)
		return
	}
	writeData(w, http.StatusOK, rev)
}

// PublicReviewsForPlan — GET /v1/plans/{id}/reviews
// Endpoint público (sem auth) que devolve até 20 reviews visíveis +
// aggregate pra renderizar social proof no SSR das páginas de plano.
func (h *Handlers) PublicReviewsForPlan(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "id")
	if planID == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	reviews, err := h.Reviews.ListByPlan(r.Context(), planID, 20)
	if err != nil {
		writeError(w, err)
		return
	}
	agg, err := h.Reviews.AggregateByPlan(r.Context(), planID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"reviews":   reviews,
		"aggregate": agg, // nil quando 0 reviews — front omite o bloco
	})
}

// --- Admin: reviews moderation ---

// AdminListReviews — GET /v1/admin/reviews
// Query: ?only_hidden=1&plan_id=...&category=...&limit=200
func (h *Handlers) AdminListReviews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := domain.AdminReviewFilter{
		OnlyHidden: q.Get("only_hidden") == "1" || q.Get("only_hidden") == "true",
		PlanID:     strings.TrimSpace(q.Get("plan_id")),
		Category:   strings.TrimSpace(q.Get("category")),
	}
	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	list, err := h.Reviews.AdminList(r.Context(), filter, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// AdminPatchReviewVisibility — PATCH /v1/admin/reviews/{id}
// Body: { "visible": true|false }
func (h *Handlers) AdminPatchReviewVisibility(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Visible *bool `json:"visible"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Visible == nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Reviews.SetVisibility(r.Context(), id, *body.Visible); err != nil {
		writeError(w, err)
		return
	}
	rev, err := h.Reviews.AdminGet(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, rev)
}

// PublicReviewsForCategory — GET /v1/categories/{code}/reviews
// Devolve só o aggregate (suficiente pro rich result no JSON-LD da página
// de listagem de categoria). Lista individual não faz sentido em /{country}/
// {category} — ali é grid de planos, social proof fica no Product detail.
func (h *Handlers) PublicReviewsForCategory(w http.ResponseWriter, r *http.Request) {
	cat := chi.URLParam(r, "code")
	if cat == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	agg, err := h.Reviews.AggregateByCategory(r.Context(), cat)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"aggregate": agg, // nil quando 0 reviews — front omite o bloco
	})
}
