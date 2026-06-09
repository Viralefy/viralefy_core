package observability

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
)

// InitSentry inicializa o cliente Sentry — captura panics, erros e mensagens
// estruturadas. No-op quando SENTRY_DSN está vazio (HML/POC opera assim por
// padrão; basta definir DSN no .env quando quiser começar a coletar).
//
// Devolve um shutdown func que faz flush de até 2s (chamado no encerramento
// gracioso do servidor pra não perder eventos em-flight).
func InitSentry(serviceName, version, environment string) func(context.Context) error {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		return func(context.Context) error { return nil }
	}
	if environment == "" {
		environment = "production"
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Release:          serviceName + "@" + version,
		Environment:      environment,
		AttachStacktrace: true,
		// Sample 100% em HML pra debugging; reduzir pra 10-20% em escala.
		TracesSampleRate: 0,
		// Filtra dados que sempre vazariam em logs.
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			if req := event.Request; req != nil {
				delete(req.Headers, "Authorization")
				delete(req.Headers, "Cookie")
				delete(req.Headers, "X-Api-Key")
			}
			return event
		},
	})
	if err != nil {
		// Falha ao inicializar é warn (não fatal) — degrada com graça pra no-op.
		return func(context.Context) error { return nil }
	}
	return func(ctx context.Context) error {
		deadline := 2 * time.Second
		if dl, ok := ctx.Deadline(); ok {
			if d := time.Until(dl); d > 0 && d < deadline {
				deadline = d
			}
		}
		sentry.Flush(deadline)
		return nil
	}
}

// SentryMiddleware devolve o handler que captura panics nas rotas e enrich
// com info da request. Plugue no chi com r.Use(SentryMiddleware()).
// No-op funcional quando SENTRY_DSN vazio: ainda recover, sem report.
func SentryMiddleware() func(http.Handler) http.Handler {
	h := sentryhttp.New(sentryhttp.Options{
		Repanic:         true, // chi/middleware.Recoverer já cuida do response 500
		WaitForDelivery: false,
		Timeout:         2 * time.Second,
	})
	return h.Handle
}
