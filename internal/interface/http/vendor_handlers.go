package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// Handlers admin pra multi-vendor scaffold (Fase 7.4). CRUD básico — só
// superadmin/managers tocam aqui (rota protegida por PermAdminsManage).
//
// Audit log: usa h.logAudit pra registrar antes/depois e dar trilha pra
// compliance financeiro quando settlement split entrar em v2.5.

func (h *Handlers) AdminListVendors(w http.ResponseWriter, r *http.Request) {
	if h.Vendors == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	list, err := h.Vendors.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

func (h *Handlers) AdminCreateVendor(w http.ResponseWriter, r *http.Request) {
	if h.Vendors == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	var body application.CreateVendorInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	v, err := h.Vendors.Create(r.Context(), body)
	if err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "create", "vendor", v.ID, nil, v)
	writeData(w, http.StatusCreated, v)
}

func (h *Handlers) AdminUpdateVendor(w http.ResponseWriter, r *http.Request) {
	if h.Vendors == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	id := chi.URLParam(r, "id")
	var body application.UpdateVendorInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	before, _ := h.Vendors.Get(r.Context(), id)
	v, err := h.Vendors.Update(r.Context(), id, body)
	if err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "update", "vendor", id, before, v)
	writeData(w, http.StatusOK, v)
}
