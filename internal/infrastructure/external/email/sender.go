// Package email é o adapter (ACL) de saída para envio de e-mail via SMTP.
// Implementa application.EmailSender. Quando o SMTP não está configurado,
// usa um sender que apenas loga (útil em dev), nunca registrando a senha.
package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

type Config struct {
	Provider string // "resend" usa a API do Resend; senão SMTP.

	// SMTP
	Addr     string // host:port (ex.: smtp.gmail.com:587). Vazio = log-only.
	User     string
	Pass     string
	From     string
	FromName string

	// Resend
	ResendAPIKey   string
	ResendFrom     string
	ResendFromName string
	ResendBaseURL  string // default https://api.resend.com (configurável p/ testes)
}

// New escolhe o EmailSender: Resend (se EMAIL_PROVIDER=resend e há API key),
// senão SMTP (se há Addr), senão LogSender (dev).
func New(cfg Config) application.EmailSender {
	if cfg.Provider == "resend" && strings.TrimSpace(cfg.ResendAPIKey) != "" {
		base := cfg.ResendBaseURL
		if base == "" {
			base = "https://api.resend.com"
		}
		from := cfg.ResendFrom
		if from == "" {
			from = "onboarding@resend.dev"
		}
		observability.L().Info("email provider selected", "provider", "resend", "from", from)
		return &ResendSender{apiKey: cfg.ResendAPIKey, from: from, fromName: cfg.ResendFromName, baseURL: base}
	}
	if strings.TrimSpace(cfg.Addr) == "" {
		observability.L().Warn("email provider not configured; using LogSender (dev only)")
		return &LogSender{}
	}
	if cfg.From == "" {
		cfg.From = "no-reply@viralefy.local"
	}
	observability.L().Info("email provider selected", "provider", "smtp", "addr", cfg.Addr)
	return &SMTPSender{cfg: cfg}
}

// SMTPSender envia via net/smtp.SendMail, que faz STARTTLS automaticamente
// quando o servidor anuncia (porta 587) e autentica quando há credenciais.
type SMTPSender struct{ cfg Config }

func (s *SMTPSender) Send(_ context.Context, msg application.EmailMessage) error {
	host := s.cfg.Addr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, host)
	}
	from := s.cfg.From
	fromHeader := from
	if s.cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", s.cfg.FromName, from)
	}
	body := msg.TextBody
	if body == "" {
		body = msg.HTMLBody
	}
	raw := buildMessage(fromHeader, msg.To, msg.Subject, body)
	return smtp.SendMail(s.cfg.Addr, auth, from, []string{msg.To}, raw)
}

func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// LogSender apenas registra que um e-mail seria enviado, sem expor o corpo
// (que pode conter a senha gerada). Usado quando não há SMTP configurado.
type LogSender struct{}

func (l *LogSender) Send(_ context.Context, msg application.EmailMessage) error {
	// PII: mascaramos o destinatário pra não vazar e-mail em log estruturado.
	observability.L().Info("email (log-only)",
		"to_masked", observability.MaskEmail(msg.To),
		"subject", msg.Subject,
	)
	return nil
}
