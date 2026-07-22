package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

	// pgx.ErrNoRows e canonico em queries que esperam exatamente 1 row e
	// vem zero. Repos as vezes esquecem de traduzir pra domain.ErrNotFound
	// (ex.: PublicListPaymentMethods quando o plan UUID nao existe — round
	// 20 simulated test descobriu 500 onde devia ser 404). Defensivo aqui.
	if errors.Is(err, pgx.ErrNoRows) {
		body := errorBody{}
		body.Error.Code = "NOT_FOUND"
		body.Error.Message = "resource not found"
		body.Error.TraceID = trace
		body.Error.Details = []interface{}{}
		writeJSON(w, http.StatusNotFound, body)
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
		// Tira o sufixo ": conflict" quando o err vem de fmt.Errorf("...: %w", ErrConflict).
		// Fica "email already registered" em vez de "email already registered: conflict".
		code, status = "CONFLICT", http.StatusConflict
		msg = strings.TrimSuffix(err.Error(), ": "+domain.ErrConflict.Error())
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

// writeErrorMsg responde um erro de BORDA, com código e mensagem escolhidos.
//
// O quê: mesmo envelope de writeError (code/message/trace_id/details), mas pra
//
//	falha que não vem de um erro de domínio — tipicamente parâmetro
//	malformado, onde 500 seria mentira e "internal server error" esconderia
//	do cliente o que ele precisa corrigir.
//
// Onde:  handlers que validam query string/params antes de chamar o domínio
//
//	(ex.: AdminListUsers com cursor inválido).
//
// Entradas: `status` (4xx), `code` (constante em SCREAMING_SNAKE), `msg`
//
//	(mensagem segura — nunca eco de input cru nem detalhe interno).
//
// Efeitos: escreve na resposta HTTP.
func writeErrorMsg(w http.ResponseWriter, status int, code, msg string) {
	body := errorBody{}
	body.Error.Code = code
	body.Error.Message = msg
	body.Error.TraceID = uuid.New().String()
	body.Error.Details = []interface{}{}
	writeJSON(w, status, body)
}
