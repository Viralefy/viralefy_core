// Package http — internal endpoints (PHASE-8 §1).
//
// Estes handlers são consumidos PELO microserviço viralefy_payments via
// loopback (callback do webhook validado lá). NÃO devem ficar expostos no
// Caddy — o reverse-proxy externo só roteia /v1/*; o /internal/* fica
// bind-localhost. Defense-in-depth: header X-Internal-Token igual ao
// segredo compartilhado em /etc/viralefy/.env.
package http

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

const internalTokenHeader = "X-Internal-Token"

// InternalTokenAuth é o middleware das rotas /internal/v1/*. Rejeita 401
// quando o header X-Internal-Token está ausente ou diferente do secret. Quando
// secret == "" — modo HML/POC sem microservice integrado — o middleware
// retorna 503: a rota existe mas não está habilitada. Isso evita que um
// deploy mal configurado aceite callbacks sem token por engano.
//
// Comparação em constant-time pra não vazar info via timing (cliente
// remoto poderia descobrir prefixos com side-channel de latência).
func InternalTokenAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secret == "" {
				// Servidor não configurado; manter rota off pra não aceitar
				// requests anônimas. 503 sinaliza "depender, voltar depois".
				w.Header().Set("Retry-After", "60")
				writeError(w, domain.ErrInvalidInput)
				return
			}
			got := r.Header.Get(internalTokenHeader)
			if got == "" {
				writeError(w, domain.ErrUnauthorized)
				return
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
				writeError(w, domain.ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// InternalPaymentConfirmed — POST /internal/v1/payment-confirmed
//
// Callback acionado pelo viralefy_payments após VALIDAR a assinatura do
// webhook do gateway (Stripe/Heleket/Woovi) e gravar idempotência interna.
// O monolito não revalida assinatura — confiamos no microserviço (loopback
// + token). Aqui só transitamos o pedido pra paid e disparamos os hooks
// pós-pagamento (email, ticket, telegram, referral payout).
//
// Body:
//
//	{
//	  "order_id":     "...",   // pode vir vazio se gateway só tem external_ref
//	  "external_ref": "...",   // id da cobrança no provider
//	  "provider":     "stripe" // opcional, info pra log
//	}
//
// Resolução: prefere order_id se presente (mais rápido, lookup direto).
// Fallback pra ConfirmByExternalRef quando vazio (Woovi/Heleket que só
// trazem external_ref no payload).
//
// Idempotência: MarkOrderPaid/ConfirmByExternalRef já são idempotentes
// (status guard). Stripe re-entrega em 5xx; este endpoint tolera repeats.
func (h *Handlers) InternalPaymentConfirmed(w http.ResponseWriter, r *http.Request) {
	logger := observability.FromContext(r.Context())
	var body struct {
		OrderID     string `json:"order_id"`
		ExternalRef string `json:"external_ref"`
		Provider    string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if body.OrderID == "" && body.ExternalRef == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if h.PaymentReceiver == nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}

	// Métrica reaproveitada (gateway_callbacks_total) — mesma família dos
	// webhooks legados. label "internal_callback" diferencia da fonte.
	provider := body.Provider
	if provider == "" {
		provider = "unknown"
	}

	if body.OrderID != "" {
		if err := h.PaymentReceiver.MarkOrderPaid(r.Context(), body.OrderID); err != nil {
			observability.GatewayCallbacksTotal.WithLabelValues(provider, "internal_confirm_failed").Inc()
			logger.Error("internal payment-confirmed MarkOrderPaid failed",
				"order_id", body.OrderID,
				"external_ref", body.ExternalRef,
				"provider", provider,
				"error", err.Error(),
			)
			// 500 pra que o microserviço possa retentar; idempotência já cobre dup.
			writeError(w, err)
			return
		}
		observability.GatewayCallbacksTotal.WithLabelValues(provider, "internal_confirmed").Inc()
		logger.Info("internal payment-confirmed via order_id",
			"order_id", body.OrderID,
			"external_ref", body.ExternalRef,
			"provider", provider,
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Fallback: só external_ref (webhook Woovi/Heleket).
	kind, err := h.PaymentReceiver.ConfirmByExternalRef(r.Context(), body.ExternalRef)
	if err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues(provider, "internal_confirm_failed").Inc()
		logger.Error("internal payment-confirmed ConfirmByExternalRef failed",
			"external_ref", body.ExternalRef,
			"provider", provider,
			"error", err.Error(),
		)
		writeError(w, err)
		return
	}
	observability.GatewayCallbacksTotal.WithLabelValues(provider, "internal_confirmed").Inc()
	logger.Info("internal payment-confirmed via external_ref",
		"external_ref", body.ExternalRef,
		"provider", provider,
		"resolved_kind", kind,
	)
	w.WriteHeader(http.StatusNoContent)
}
