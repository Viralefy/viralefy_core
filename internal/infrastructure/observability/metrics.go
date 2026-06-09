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
