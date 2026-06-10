package application

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// StripeReconcileCron faz polling periódico de orders Stripe pendentes pra
// pegar pagamentos cujo webhook caiu (rede instável, retry esgotado, race
// com bug do Caddy bloqueando /internal/v1 — agora resolvido mas o legado
// historicamente já comeu webhook).
//
// Política:
//   - A cada Interval (default 5min), busca até 50 orders status IN ('pending',
//     'received') com provider=stripe, com external_ref tipo 'cs_live_…' ou
//     'cs_test_…', criadas entre 10min e 72h atrás.
//       * <10min: webhook pode estar a caminho, não polua a Stripe API.
//       * >72h: order provavelmente cancelada/expirada, não vale ressuscitar.
//   - Pra cada uma: GET https://api.stripe.com/v1/checkout/sessions/{id} com
//     o secret_key do gateway daquela order.
//   - Se payment_status == "paid", chama PaymentReceiver.ConfirmByExternalRef
//     (mesmo caminho do webhook → marca order paid + dispara hooks).
//
// Falhas por order não derrubam o tick. Sucesso é log info; rate-limit
// 429 da Stripe pausa o tick e tenta no próximo. Nunca lê o body do POST
// pra evitar logar secret accidentally.
type StripeReconcileCron struct {
	DB       *postgres.DB
	Receiver *PaymentReceiver
	Interval time.Duration // default 5min

	// httpClient é injetado pra teste; default 20s timeout.
	httpClient *http.Client

	running atomic.Bool
	stopped chan struct{}
}

// stripeSession é o pedaço do payload da Stripe que importa pra reconcile.
type stripeSession struct {
	ID            string `json:"id"`
	PaymentStatus string `json:"payment_status"` // "paid" | "unpaid" | "no_payment_required"
	Status        string `json:"status"`         // "complete" | "expired" | "open"
}

func (c *StripeReconcileCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	c.stopped = make(chan struct{})
	go c.loop(ctx)
}

func (c *StripeReconcileCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *StripeReconcileCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "stripe_reconcile")
	logger.Info("stripe reconcile cron started", "interval", c.Interval.String())

	// Primeiro tick em 30s pra dar tempo do app subir e estabilizar conexões.
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("stripe reconcile cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx)
			t.Reset(c.Interval)
		}
	}
}

func (c *StripeReconcileCron) tick(ctx context.Context) {
	logger := observability.FromContext(ctx).With("cron", "stripe_reconcile")
	tickCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// start marca o início pra duração final. Mesmo quando a query inicial
	// falha (return early abaixo), NÃO marcamos last_run_timestamp — o SLO
	// pede "última run *bem-sucedida*", então abortos viram silêncio na
	// gauge (e o alerta absent/stale dispara, que é o comportamento certo).
	start := time.Now()

	rows, err := c.DB.Pool().Query(tickCtx, `
		SELECT o.id, o.external_ref, g.config->>'secret_key' AS secret_key
		FROM orders o
		JOIN payment_gateways g ON g.id = o.gateway_id
		WHERE g.provider = 'stripe'
		  AND o.status IN ('pending')
		  AND o.external_ref IS NOT NULL
		  AND o.external_ref LIKE 'cs_%'
		  AND o.created_at <  NOW() - INTERVAL '10 minutes'
		  AND o.created_at >= NOW() - INTERVAL '72 hours'
		ORDER BY o.created_at ASC
		LIMIT 50`)
	if err != nil {
		observability.StripeReconcileErrors.WithLabelValues("query").Inc()
		logger.Warn("query pending orders failed", "error", err.Error())
		return
	}
	defer rows.Close()

	type orderToCheck struct {
		ID        string
		Ref       string
		SecretKey string
	}
	var batch []orderToCheck
	for rows.Next() {
		var o orderToCheck
		if err := rows.Scan(&o.ID, &o.Ref, &o.SecretKey); err != nil {
			logger.Warn("scan failed", "error", err.Error())
			continue
		}
		if o.SecretKey == "" || o.Ref == "" {
			continue
		}
		batch = append(batch, o)
	}
	if err := rows.Err(); err != nil {
		observability.StripeReconcileErrors.WithLabelValues("query").Inc()
		logger.Warn("rows iter failed", "error", err.Error())
	}
	if len(batch) == 0 {
		// Batch vazio ainda é "tick OK" — sem trabalho a fazer significa que
		// o sistema está saudável (webhooks chegaram). Marca timestamp pro SLO.
		observability.StripeReconcileLastRunTimestamp.SetToCurrentTime()
		observability.StripeReconcileLastRunDurationMs.Set(float64(time.Since(start).Milliseconds()))
		return
	}
	logger.Info("reconcile tick scanning", "batch_size", len(batch))

	confirmed := 0
	rateLimited := false
	for _, o := range batch {
		if rateLimited {
			break
		}
		observability.StripeReconcileOrdersChecked.Inc()
		paid, status, err := c.lookupSession(tickCtx, o.SecretKey, o.Ref)
		if err != nil {
			if strings.Contains(err.Error(), "HTTP 429") {
				rateLimited = true
				observability.StripeReconcileErrors.WithLabelValues("rate_limited").Inc()
				logger.Warn("stripe rate-limited, deferring batch tail to next tick",
					"checked_so_far", confirmed,
				)
				break
			}
			// 404 da Stripe = session foi expirada/deletada — sem nada a fazer.
			// Não conta como erro (esperado, não polui counter).
			if strings.Contains(err.Error(), "HTTP 404") {
				continue
			}
			observability.StripeReconcileErrors.WithLabelValues("lookup").Inc()
			logger.Warn("stripe session lookup failed",
				"order_id", o.ID,
				"external_ref", o.Ref,
				"error", err.Error(),
			)
			continue
		}
		if !paid {
			continue
		}
		if c.Receiver == nil {
			logger.Warn("receiver nil — pulando confirm", "order_id", o.ID)
			continue
		}
		if _, err := c.Receiver.ConfirmByExternalRef(tickCtx, o.Ref); err != nil {
			observability.StripeReconcileErrors.WithLabelValues("confirm").Inc()
			logger.Warn("confirm failed",
				"order_id", o.ID,
				"external_ref", o.Ref,
				"error", err.Error(),
			)
			continue
		}
		confirmed++
		observability.StripeReconcileOrdersConfirmed.Inc()
		logger.Info("order confirmed via reconcile poll",
			"order_id", o.ID,
			"external_ref", o.Ref,
			"stripe_status", status,
		)
	}
	if confirmed > 0 {
		logger.Info("reconcile tick done",
			"checked", len(batch),
			"confirmed", confirmed,
		)
	}

	// Tick chegou ao fim (mesmo com rate_limit no meio do batch — tail será
	// processado no próximo tick, mas o tick atual *rodou* até onde pôde).
	// Marca o sinal de freshness pro SLO.
	observability.StripeReconcileLastRunTimestamp.SetToCurrentTime()
	observability.StripeReconcileLastRunDurationMs.Set(float64(time.Since(start).Milliseconds()))
}

// lookupSession chama GET /v1/checkout/sessions/{id} com Basic auth (secret
// como username, password vazio — convenção Stripe). Retorna paid bool +
// status string + erro.
func (c *StripeReconcileCron) lookupSession(ctx context.Context, secret, sessionID string) (bool, string, error) {
	if secret == "" || sessionID == "" {
		return false, "", fmt.Errorf("stripe reconcile: secret or session_id empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.stripe.com/v1/checkout/sessions/"+sessionID, nil)
	if err != nil {
		return false, "", err
	}
	req.SetBasicAuth(secret, "")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "", fmt.Errorf("stripe reconcile: HTTP %d", resp.StatusCode)
	}
	var s stripeSession
	if err := json.Unmarshal(body, &s); err != nil {
		return false, "", fmt.Errorf("stripe reconcile: decode: %w", err)
	}
	return s.PaymentStatus == "paid", s.Status, nil
}
