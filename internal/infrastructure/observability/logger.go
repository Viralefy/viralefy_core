// Package observability — logger, metrics e tracing para o viralefy_core.
//
// Logger: slog em JSON pra stderr. Inclui helpers de mascaramento de PII
// (e-mail, CPF, telefone) — proibido logar dados sensíveis em claro (§16.1
// das diretrizes).
package observability

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// Inicializado em InitLogger; default = stderr JSON com level INFO.
var defaultLogger *slog.Logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

// LoggerConfig: knobs externos pro logger global.
type LoggerConfig struct {
	Level     slog.Level
	Service   string
	Version   string
	Component string
}

// InitLogger configura o slog global em JSON, com campos service/version.
// Idempotente: chamar de novo sobrescreve. Devolve o *slog.Logger configurado
// pra quem quiser usar como child logger.
func InitLogger(cfg LoggerConfig) *slog.Logger {
	if cfg.Service == "" {
		cfg.Service = "viralefy-api"
	}
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: cfg.Level,
	})
	l := slog.New(h).With(
		slog.String("service", cfg.Service),
		slog.String("version", cfg.Version),
		slog.String("component", cfg.Component),
	)
	defaultLogger = l
	slog.SetDefault(l)
	return l
}

// L devolve o logger global. Curto pra usar inline.
func L() *slog.Logger { return defaultLogger }

// ---------- Context plumbing ---------- //

type ctxKey int

const (
	loggerKey ctxKey = iota
	traceIDKey
	requestIDKey
)

// WithLogger guarda um logger derivado no contexto. Útil para enriquecer com
// trace_id / request_id sem mutar o global.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext devolve o logger do contexto (ou o global se nada gravado).
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return defaultLogger
	}
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return defaultLogger
}

// WithTraceID grava trace_id no contexto pra ser puxado pelo response.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFromContext devolve trace_id, ou "" se nada.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(traceIDKey).(string); ok {
		return id
	}
	return ""
}

// WithRequestID grava request_id no contexto.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestIDFromContext devolve request_id, ou "".
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// ---------- PII masking (diretrizes §16.1) ---------- //

// MaskEmail: u***@example.com — preserva domínio para debug, oculta local part.
func MaskEmail(e string) string {
	e = strings.TrimSpace(e)
	if e == "" {
		return ""
	}
	at := strings.IndexByte(e, '@')
	if at < 1 {
		return "***"
	}
	local := e[:at]
	domain := e[at:]
	if len(local) <= 1 {
		return local + "***" + domain
	}
	return string(local[0]) + "***" + domain
}

// MaskPhone: mantém DDI/DDD + últimos 2 dígitos.
func MaskPhone(p string) string {
	digits := digitsOnly(p)
	if len(digits) <= 4 {
		return "***"
	}
	return digits[:2] + strings.Repeat("*", len(digits)-4) + digits[len(digits)-2:]
}

var cpfRe = regexp.MustCompile(`\b\d{3}\.?\d{3}\.?\d{3}-?\d{2}\b`)

// MaskCPF substitui CPFs em uma string por ***.***.***-**.
func MaskCPF(s string) string {
	return cpfRe.ReplaceAllString(s, "***.***.***-**")
}

// MaskToken oculta tokens longos: prefix + comprimento.
func MaskToken(t string) string {
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "..." + "(len=" + itoa(len(t)) + ")"
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
