package application

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// PlanPriceDriftCron monitora rows em plan_prices que estão fora da fórmula
// canônica (USD/100 * currency.rate). Drift > 0 sinaliza que alguma rate
// mudou sem o cascade rodar (regressão 2026-06-06), ou que admin escreveu
// manual override (esperado, mas vale aparecer no Grafana).
//
// NÃO faz auto-fix — só observa. Decisão de corrigir é do operador, porque
// pode ser override intencional.
type PlanPriceDriftCron struct {
	DB       *postgres.DB
	Interval time.Duration // default 1h

	running atomic.Bool
	stopped chan struct{}
}

func (c *PlanPriceDriftCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = 1 * time.Hour
	}
	c.stopped = make(chan struct{})
	go c.loop(ctx)
}

func (c *PlanPriceDriftCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *PlanPriceDriftCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "plan_price_drift")
	logger.Info("plan price drift cron started", "interval", c.Interval.String())

	c.tick(ctx)
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("plan price drift cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

func (c *PlanPriceDriftCron) tick(ctx context.Context) {
	logger := observability.FromContext(ctx).With("cron", "plan_price_drift")
	tickCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Conta por moeda: rows onde |actual - (USD * rate)| > tolerância da moeda
	// (1 unidade da última casa decimal). Tolerância garante que arredondamento
	// não dispare falsos positivos.
	// `c.rate` é double precision na schema; cast pra numeric pra que
	// ROUND(numeric, integer) seja resolvido (Postgres não tem overload
	// ROUND(double, int)).
	rows, err := c.DB.Pool().Query(tickCtx, `
		SELECT
			c.code,
			COUNT(*) FILTER (
				WHERE pp.amount::numeric IS DISTINCT FROM
				      ROUND((p.price_cents::numeric / 100.0) * c.rate::numeric, c.decimals)
			) AS drift_rows
		FROM plan_prices pp
		JOIN plans p      ON p.id = pp.plan_id
		JOIN currencies c ON c.code = pp.currency_code
		WHERE pp.amount ~ '^[0-9]+(\.[0-9]+)?$'
		GROUP BY c.code`)
	if err != nil {
		logger.Warn("drift query failed", "error", err.Error())
		return
	}
	defer rows.Close()

	totalDrift := int64(0)
	driftedCodes := make([]string, 0, 4)
	for rows.Next() {
		var code string
		var drift int64
		if err := rows.Scan(&code, &drift); err != nil {
			logger.Warn("drift scan failed", "error", err.Error())
			continue
		}
		observability.PlanPriceDriftRows.WithLabelValues(code).Set(float64(drift))
		if drift > 0 {
			logger.Warn("plan_prices drift detected",
				"currency_code", code,
				"rows", strconv.FormatInt(drift, 10),
			)
			driftedCodes = append(driftedCodes, code)
		}
		totalDrift += drift
	}
	if err := rows.Err(); err != nil {
		logger.Warn("drift rows iter failed", "error", err.Error())
	}
	if totalDrift == 0 {
		logger.Info("plan_prices consistent across all currencies")
		return
	}

	// Pra cada moeda com drift, busca até 10 plan_ids amostrados pra log —
	// post-mortem (BTC 2026-06-11) mostrou que SÓ o count não basta: ops gasta
	// SQL ad-hoc reconstruindo a lista. Causa típica do drift: admin salva
	// plano pela UI com `prices` no payload contendo valor stale por moeda
	// (front carregou form antes de currency rate cascade rodar); UpsertPrices
	// sobrescreve baseline recém-recomputado em PlanService.Update.
	for _, code := range driftedCodes {
		c.logDriftSamples(tickCtx, logger, code)
	}
}

// logDriftSamples emite até 10 plan_ids da moeda em drift. Read-only, não
// muta plan_prices — fix é manual (admin re-edita o plano OU força cascade
// via UpdateRate da moeda).
func (c *PlanPriceDriftCron) logDriftSamples(ctx context.Context, logger interface {
	Warn(string, ...any)
}, code string) {
	rows, err := c.DB.Pool().Query(ctx, `
		SELECT pp.plan_id, pp.amount,
		       ROUND((p.price_cents::numeric / 100.0) * c.rate::numeric, c.decimals) AS expected
		FROM plan_prices pp
		JOIN plans p      ON p.id = pp.plan_id
		JOIN currencies c ON c.code = pp.currency_code
		WHERE pp.currency_code = $1
		  AND pp.amount ~ '^[0-9]+(\.[0-9]+)?$'
		  AND pp.amount::numeric IS DISTINCT FROM
		      ROUND((p.price_cents::numeric / 100.0) * c.rate::numeric, c.decimals)
		LIMIT 10`, code)
	if err != nil {
		logger.Warn("drift samples query failed", "currency_code", code, "error", err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var planID, stored, expected string
		if err := rows.Scan(&planID, &stored, &expected); err != nil {
			continue
		}
		logger.Warn("plan_prices drift sample",
			"currency_code", code,
			"plan_id", planID,
			"stored", stored,
			"expected", expected,
		)
	}
}
