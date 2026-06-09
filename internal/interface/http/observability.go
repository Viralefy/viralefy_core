package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/trace"

	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// ObservabilityMiddleware faz:
//   - extrai trace_id do contexto OTel (preenchido por otelhttp)
//   - extrai request_id (chi/middleware.RequestID)
//   - cria child logger com trace_id/request_id/method/path
//   - cronometra a request
//   - ao fim: incrementa http_requests_total + observa duração + emite log JSON
//
// Pre-condição: deve rodar DEPOIS de otelhttp.NewHandler e
// middleware.RequestID, pra ter o trace context e o request_id no ctx.
func ObservabilityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// trace_id vem do span ativo (otelhttp instrumenta o handler).
		span := trace.SpanFromContext(r.Context())
		traceID := ""
		if sc := span.SpanContext(); sc.HasTraceID() {
			traceID = sc.TraceID().String()
		}

		// request_id vem do middleware.RequestID do chi.
		requestID := middleware.GetReqID(r.Context())

		// Logger derivado com os IDs — handlers podem puxar via observability.FromContext(ctx).
		l := observability.L().With(
			"trace_id", traceID,
			"request_id", requestID,
			"method", r.Method,
		)
		ctx := observability.WithLogger(r.Context(), l)
		ctx = observability.WithTraceID(ctx, traceID)
		ctx = observability.WithRequestID(ctx, requestID)
		r = r.WithContext(ctx)

		// WrapResponseWriter pra capturar status code sem código duplicado.
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			dur := time.Since(start)

			// chi.RouteContext só é populado durante a request; capturamos aqui
			// (ainda dentro do defer, antes do handler retornar de fato — mas
			// como wrap só termina depois do servewall, está OK em chi).
			pathLabel := chi.RouteContext(r.Context()).RoutePattern()
			if pathLabel == "" {
				pathLabel = "unknown"
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			statusStr := strconv.Itoa(status)

			observability.HTTPRequestsTotal.WithLabelValues(r.Method, pathLabel, statusStr).Inc()
			observability.HTTPRequestDurationSeconds.WithLabelValues(r.Method, pathLabel).Observe(dur.Seconds())

			// Logger final inclui status + duration + size + path resolvido.
			// Não logamos query string nem body — pode conter PII.
			l.Info("http_request",
				"path", pathLabel,
				"status", status,
				"duration_ms", dur.Milliseconds(),
				"bytes", ww.BytesWritten(),
				"remote_ip", r.RemoteAddr,
				"user_agent", r.Header.Get("User-Agent"),
			)
		}()

		next.ServeHTTP(ww, r)
	})
}
