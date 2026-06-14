package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type errorBody struct {
	Error struct {
		Code    string        `json:"code"`
		Message string        `json:"message"`
		TraceID string        `json:"trace_id"`
		Details []interface{} `json:"details"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	trace := uuid.New().String()
	code := "INTERNAL_ERROR"
	status := http.StatusInternalServerError
	msg := "internal server error"

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		body := errorBody{}
		body.Error.Code = "CONFLICT"
		body.Error.Message = "resource already exists"
		body.Error.TraceID = trace
		body.Error.Details = []interface{}{}
		writeJSON(w, http.StatusConflict, body)
		return
	}

	switch {
	case errors.Is(err, domain.ErrNotFound):
		code, status, msg = "NOT_FOUND", http.StatusNotFound, err.Error()
	case errors.Is(err, domain.ErrInvalidInput):
		code, status, msg = "INVALID_INPUT", http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, domain.ErrUnauthorized):
		code, status, msg = "UNAUTHORIZED", http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, domain.ErrForbidden):
		code, status, msg = "FORBIDDEN", http.StatusForbidden, "forbidden"
	case errors.Is(err, domain.ErrConflict):
		code, status, msg = "CONFLICT", http.StatusConflict, err.Error()
	case errors.Is(err, domain.ErrNotImplemented):
		code, status, msg = "NOT_IMPLEMENTED", http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, domain.ErrCouponNotFound):
		code, status, msg = "COUPON_NOT_FOUND", http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, domain.ErrCouponInactive),
		errors.Is(err, domain.ErrCouponNotYetValid),
		errors.Is(err, domain.ErrCouponExpired),
		errors.Is(err, domain.ErrCouponMinOrderNotMet),
		errors.Is(err, domain.ErrCouponCategoryNotAllowed),
		errors.Is(err, domain.ErrCouponMaxUsesReached),
		errors.Is(err, domain.ErrCouponFirstOrderOnly):
		code, status, msg = "COUPON_INVALID", http.StatusUnprocessableEntity, err.Error()
	}

	body := errorBody{}
	body.Error.Code = code
	body.Error.Message = msg
	body.Error.TraceID = trace
	body.Error.Details = []interface{}{}
	writeJSON(w, status, body)
}

func writeData(w http.ResponseWriter, status int, data interface{}) {
	writeJSON(w, status, map[string]interface{}{"data": data})
}
