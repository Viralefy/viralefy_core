package application

import (
	"context"
	"fmt"
	"html"
	"sync/atomic"
	"time"

	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// CartAbandonmentCron varre orders pendentes entre 1h e 24h após criação
// (com payment_url já gerado) e dispara um lembrete por e-mail com o link
// pra completar o checkout em 1 clique. Marca a coluna
// abandonment_email_sent_at pra não enviar duas vezes.
//
// Política:
//   - Tick a cada `Interval` (padrão 30min — janela curta o suficiente pra
//     pegar todos os pedidos abandonados dentro do bucket de 1-24h sem
//     spammar o gateway de e-mail).
//   - Janela 1-24h: dar 1h pro usuário voltar sozinho, e cortar em 24h
//     pra não parecer creepy ("você esqueceu há 3 dias").
//   - Batch fixo de 50 por tick. Suficiente pra absorver picos e ainda
//     ficar bem abaixo do limite do Resend (100/s soft).
//   - Falhas individuais log warn e seguem — 1 e-mail ruim não derruba o tick.
//   - Idempotência: o WHERE já filtra abandonment_email_sent_at IS NULL,
//     então re-runs naturalmente pulam quem já recebeu.
type CartAbandonmentCron struct {
	DB       *postgres.DB
	Email    EmailSender
	SiteURL  string
	Interval time.Duration // entre ticks; default 30min

	running atomic.Bool
	stopped chan struct{}
}

// NewCartAbandonmentCron construtor padrão.
func NewCartAbandonmentCron(db *postgres.DB, email EmailSender, siteURL string) *CartAbandonmentCron {
	return &CartAbandonmentCron{
		DB:      db,
		Email:   email,
		SiteURL: siteURL,
	}
}

func (c *CartAbandonmentCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = 30 * time.Minute
	}
	c.stopped = make(chan struct{})
	go c.loop(ctx)
}

func (c *CartAbandonmentCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *CartAbandonmentCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "cart_abandonment")
	logger.Info("cart abandonment cron started", "interval", c.Interval.String())

	c.tick(ctx)

	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("cart abandonment cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

type abandonmentCandidate struct {
	OrderID         string
	PaymentURL      string
	DisplayAmount   string
	DisplayCurrency string
	PlanID          string
	PlanName        string
	UserEmail       string
	UserName        string
}

func (c *CartAbandonmentCron) tick(ctx context.Context) {
	logger := observability.FromContext(ctx).With("cron", "cart_abandonment")
	tickCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	rows, err := c.DB.Pool().Query(tickCtx, `
		SELECT o.id, o.payment_url, o.display_amount, o.display_currency,
		       o.plan_id, p.name AS plan_name,
		       u.email, u.name
		FROM orders o
		JOIN users u ON u.id = o.user_id
		JOIN plans p ON p.id = o.plan_id
		WHERE o.status = 'pending'
		  AND o.abandonment_email_sent_at IS NULL
		  AND o.payment_url IS NOT NULL
		  AND o.created_at < NOW() - INTERVAL '1 hour'
		  AND o.created_at > NOW() - INTERVAL '24 hours'
		LIMIT 50`)
	if err != nil {
		logger.Warn("list abandoned failed", "error", err.Error())
		return
	}

	var candidates []abandonmentCandidate
	for rows.Next() {
		var cand abandonmentCandidate
		if err := rows.Scan(
			&cand.OrderID, &cand.PaymentURL, &cand.DisplayAmount, &cand.DisplayCurrency,
			&cand.PlanID, &cand.PlanName, &cand.UserEmail, &cand.UserName,
		); err != nil {
			logger.Warn("scan abandoned row failed", "error", err.Error())
			continue
		}
		candidates = append(candidates, cand)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		logger.Warn("rows iteration failed", "error", err.Error())
		return
	}
	if len(candidates) == 0 {
		return
	}
	logger.Info("processing batch", "count", len(candidates))

	var sent, failed int
	for _, cand := range candidates {
		mailCtx, cancelMail := context.WithTimeout(ctx, 30*time.Second)
		if err := c.sendOne(mailCtx, cand); err != nil {
			failed++
			logger.Warn("abandonment email failed",
				"order_id", cand.OrderID,
				"to", cand.UserEmail,
				"error", err.Error(),
			)
		} else {
			sent++
		}
		cancelMail()
	}
	logger.Info("batch done", "sent", sent, "failed", failed)
}

func (c *CartAbandonmentCron) sendOne(ctx context.Context, cand abandonmentCandidate) error {
	name := firstName(cand.UserName)
	displayPrice := cand.DisplayAmount
	if cand.DisplayCurrency != "" {
		displayPrice = fmt.Sprintf("%s %s", cand.DisplayAmount, cand.DisplayCurrency)
	}

	subject := "You left your checkout open — finish in 1 click"
	text := fmt.Sprintf(
		"Hi %s, you started checkout for %s (%s) but didn't finish. Complete in 1 click: %s",
		name, cand.PlanName, displayPrice, cand.PaymentURL,
	)
	htmlBody := fmt.Sprintf(
		`<!doctype html><html><body style="font-family:system-ui,sans-serif;max-width:520px;margin:0 auto;padding:24px;color:#111">`+
			`<p>Hi %s,</p>`+
			`<p>You started checkout for <strong>%s</strong> (%s) but didn't finish.</p>`+
			`<p><a href="%s" style="display:inline-block;background:#111;color:#fff;padding:12px 20px;border-radius:6px;text-decoration:none">Complete in 1 click</a></p>`+
			`<p style="color:#666;font-size:13px">Or paste this link in your browser:<br>%s</p>`+
			`</body></html>`,
		html.EscapeString(name),
		html.EscapeString(cand.PlanName),
		html.EscapeString(displayPrice),
		html.EscapeString(cand.PaymentURL),
		html.EscapeString(cand.PaymentURL),
	)

	if err := c.Email.Send(ctx, EmailMessage{
		To:       cand.UserEmail,
		Subject:  subject,
		TextBody: text,
		HTMLBody: htmlBody,
	}); err != nil {
		return err
	}

	// Só marca enviado quando o sender devolveu OK — retry natural no próximo
	// tick se falhar (rate-limit do Resend, downtime, etc.).
	_, err := c.DB.Pool().Exec(ctx,
		`UPDATE orders SET abandonment_email_sent_at = NOW() WHERE id = $1`,
		cand.OrderID,
	)
	return err
}
