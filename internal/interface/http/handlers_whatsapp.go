package http

import (
	"encoding/json"
	"net/http"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// MeGetWhatsAppPref — GET /v1/me/whatsapp
// Devolve { number, opt_in }. number pode ser "" quando o usuário ainda
// não cadastrou. Front usa pra hidratar o form em /account/notifications.
func (h *Handlers) MeGetWhatsAppPref(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.WhatsApp == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	pref, err := h.WhatsApp.GetPref(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, pref)
}

// MeUpdateWhatsApp — PUT /v1/me/whatsapp
// Body: { number: string, opt_in: bool }
//
// Aplica em duas etapas (UpdateNumber + OptIn) porque cada operação é
// idempotente isolada e o serviço expõe os dois ângulos pra outros
// callers no futuro. number vazio limpa o cadastro.
func (h *Handlers) MeUpdateWhatsApp(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.WhatsApp == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	var body struct {
		Number string `json:"number"`
		OptIn  bool   `json:"opt_in"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.WhatsApp.UpdateNumber(r.Context(), userID, body.Number); err != nil {
		writeError(w, err)
		return
	}
	if err := h.WhatsApp.OptIn(r.Context(), userID, body.OptIn); err != nil {
		writeError(w, err)
		return
	}
	pref, err := h.WhatsApp.GetPref(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, pref)
}
