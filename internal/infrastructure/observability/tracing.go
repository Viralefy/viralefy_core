package observability

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// TracingConfig: parâmetros do tracer provider.
type TracingConfig struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	// Endpoint OTLP HTTP. Default: $OTEL_EXPORTER_OTLP_ENDPOINT ou
	// http://127.0.0.1:4318. Aceita base URL — o path /v1/traces é
	// anexado pelo SDK.
	Endpoint string
	// SampleRatio (0..1). 1 = sample tudo (dev/MVP), 0 = nada.
	SampleRatio float64
}

// InitTracer configura o OTel TracerProvider com OTLP HTTP exporter para o
// Tempo local. Devolve uma função de shutdown — chamar antes de sair (graceful).
//
// Propagação W3C TraceContext + Baggage + B3 (defensivo, pra interoperar com
// SDKs que ainda mandem B3).
func InitTracer(ctx context.Context, cfg TracingConfig) (func(context.Context) error, error) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = "viralefy-api"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = envOr("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	}
	if cfg.SampleRatio <= 0 {
		cfg.SampleRatio = 1.0
	}
	if cfg.SampleRatio > 1 {
		cfg.SampleRatio = 1
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironment(cfg.Environment),
		),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, err
	}

	exp, err := otlptrace.New(ctx, otlptracehttp.NewClient(
		otlptracehttp.WithEndpointURL(cfg.Endpoint+"/v1/traces"),
	))
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithMaxExportBatchSize(512),
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
		b3.New(),
	))

	return tp.Shutdown, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
