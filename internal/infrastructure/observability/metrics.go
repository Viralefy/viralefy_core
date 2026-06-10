package observability

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Collectors expostos em /metrics (RED + DB + gateways).
//
// Cardinality: label "path" usa a route pattern do chi (ex.: /v1/me/orders),
// nunca a URL bruta — evita explosão por ID na URL.
var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total de requests HTTP processados, com labels method, path, status.",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duração das requests HTTP em segundos.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	DBQueryDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "db_query_duration_seconds",
			Help:    "Duração de queries SQL agrupadas por tipo lógico.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
		},
		[]string{"query_type"},
	)

	GatewayCallbacksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_callbacks_total",
			Help: "Webhooks recebidos dos gateways de pagamento, com label provider e status.",
		},
		[]string{"provider", "status"},
	)

	// PlanPriceDriftRows mede quantos rows em plan_prices têm `amount` ≠
	// (price_cents/100 * rate). Cresce → algum admin esqueceu de cascatear
	// (regressão 2026-06-06) OU alguém escreveu manual override (esperado,
	// se for poucos rows). Alerta sugerido: > 5 rows por moeda por > 12h.
	PlanPriceDriftRows = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "viralefy_plan_price_drift_rows",
			Help: "Quantidade de linhas em plan_prices fora da fórmula USD * rate por moeda.",
		},
		[]string{"currency_code"},
	)

	// ─── Stripe reconcile cron metrics ──────────────────────────────────────
	// Sinal pra SLO stripe_reconcile_freshness: cron de reconciliação roda a
	// cada 5min puxando orders Stripe pending pra cobrir webhook miss. Sem
	// essas métricas, alerta SLOStripeReconcileStale dispara via absent() —
	// o que era o TODO até este ponto.
	//
	// Cardinality é controlada: nenhum label de order_id ou session_id. Apenas
	// "type" no errors counter, fixo em {query, lookup, confirm, rate_limited}.

	// StripeReconcileLastRunTimestamp marca o final do último tick bem-sucedido
	// (rodou até o fim sem panic — falhas por order não derrubam o tick).
	// SLO: time() - <metric> < 600s.
	StripeReconcileLastRunTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "viralefy",
			Name:      "stripe_reconcile_last_run_timestamp",
			Help:      "Unix timestamp of last successful Stripe reconcile cron tick.",
		},
	)

	// StripeReconcileLastRunDurationMs ajuda a detectar tick que ficou lento
	// (Stripe API degradada, batch crescendo).
	StripeReconcileLastRunDurationMs = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "viralefy",
			Name:      "stripe_reconcile_last_run_duration_ms",
			Help:      "Duration of last Stripe reconcile cron tick in ms.",
		},
	)

	// StripeReconcileOrdersChecked = total cumulativo de orders examinadas
	// (chamou Stripe API). Rate dá throughput do cron.
	StripeReconcileOrdersChecked = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "viralefy",
			Name:      "stripe_reconcile_orders_checked_total",
			Help:      "Total orders checked by Stripe reconcile cron.",
		},
	)

	// StripeReconcileOrdersConfirmed = total cumulativo de orders confirmadas
	// via reconcile (webhook missed e cron salvou). Idealmente perto de 0.
	StripeReconcileOrdersConfirmed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "viralefy",
			Name:      "stripe_reconcile_orders_confirmed_total",
			Help:      "Total orders confirmed by Stripe reconcile (webhook missed cases).",
		},
	)

	// StripeReconcileErrors com label "type" baixa-cardinalidade:
	//   query        — falha de SELECT inicial (DB)
	//   lookup       — falha GET Stripe (não-2xx, exceto 404 que é esperado)
	//   confirm      — ConfirmByExternalRef retornou erro
	//   rate_limited — HTTP 429 da Stripe (tick aborta tail)
	StripeReconcileErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "viralefy",
			Name:      "stripe_reconcile_errors_total",
			Help:      "Total errors during Stripe reconcile by type (query|lookup|confirm|rate_limited).",
		},
		[]string{"type"},
	)
)

var (
	metricsRegistry *prometheus.Registry
	metricsOnce     sync.Once
)

// InitMetrics regista os collectors em um Registry isolado (evita duplicar
// com qualquer pacote que use o default). Idempotente.
func InitMetrics() *prometheus.Registry {
	metricsOnce.Do(func() {
		reg := prometheus.NewRegistry()
		reg.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
			HTTPRequestsTotal,
			HTTPRequestDurationSeconds,
			DBQueryDurationSeconds,
			GatewayCallbacksTotal,
			PlanPriceDriftRows,
			StripeReconcileLastRunTimestamp,
			StripeReconcileLastRunDurationMs,
			StripeReconcileOrdersChecked,
			StripeReconcileOrdersConfirmed,
			StripeReconcileErrors,
		)
		metricsRegistry = reg
	})
	return metricsRegistry
}

// MetricsHandler devolve o handler HTTP do /metrics. Use após InitMetrics.
func MetricsHandler() http.Handler {
	if metricsRegistry == nil {
		InitMetrics()
	}
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Registry:          metricsRegistry,
	})
}

// ObserveDBQuery: shorthand para instrumentar uma query SQL.
//
//	defer observability.ObserveDBQuery("select_user")(time.Now())
func ObserveDBQuery(queryType string) func(start time.Time) {
	return func(start time.Time) {
		DBQueryDurationSeconds.WithLabelValues(queryType).Observe(time.Since(start).Seconds())
	}
}
