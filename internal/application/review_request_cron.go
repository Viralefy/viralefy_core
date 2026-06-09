package application

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// ReviewRequestCron dispara o e-mail "how was your order?" pra pedidos
// pagos há pelo menos `Delay` (padrão 7d) e que ainda não tiveram esse
// email enviado nem geraram review.
//
// Por que 7d (não 24h como o delivery capture):
//   - 24h o delivery capture pega métricas — o serviço ainda está rolando.
//   - 7d o cliente já viu o resultado consolidado e tem opinião formada.
//
// Política:
//   - Roda a cada `Interval` (padrão 1h — review request não é tempo-crítico).
//   - Batch pequeno (`Batch`, padrão 50). Resend tem soft-limit de 100/s
//     e usuário paga por volume — não vale spam.
//   - Falhas individuais log warn e seguem; tick não derruba por 1 email ruim.
//   - Idempotência: ListReadyForReviewRequest filtra review_email_sent_at IS NULL,
//     então re-runs naturalmente pulam quem já recebeu.
type ReviewRequestCron struct {
	Repo     domain.ReviewRequestRepository
	Email    EmailSender
	SiteURL  string
	Interval time.Duration // entre ticks; default 1h
	Delay    time.Duration // mínimo desde paid; default 7d
	Batch    int           // máximo por tick; default 50

	running atomic.Bool
	stopped chan struct{}
}

func (c *ReviewRequestCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = 1 * time.Hour
	}
	if c.Delay <= 0 {
		c.Delay = 7 * 24 * time.Hour
	}
	if c.Batch <= 0 {
		c.Batch = 50
	}
	if c.SiteURL == "" {
		c.SiteURL = "https://viralefy.com"
	}
	c.stopped = make(chan struct{})
	go c.loop(ctx)
}

func (c *ReviewRequestCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *ReviewRequestCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "review_request")
	logger.Info("review request cron started",
		"interval", c.Interval.String(),
		"delay", c.Delay.String(),
		"batch", c.Batch,
	)

	c.tick(ctx)

	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("review request cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

func (c *ReviewRequestCron) tick(ctx context.Context) {
	logger := observability.FromContext(ctx).With("cron", "review_request")
	cutoff := time.Now().Add(-c.Delay)
	tickCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	candidates, err := c.Repo.ListReadyForReviewRequest(tickCtx, cutoff, c.Batch)
	if err != nil {
		logger.Warn("list ready failed", "error", err.Error())
		return
	}
	if len(candidates) == 0 {
		return
	}
	logger.Info("processing batch", "count", len(candidates), "cutoff", cutoff.Format(time.RFC3339))

	var sent, failed int
	for _, cand := range candidates {
		mailCtx, cancelMail := context.WithTimeout(ctx, 30*time.Second)
		if err := c.sendOne(mailCtx, cand); err != nil {
			failed++
			logger.Warn("review request email failed",
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

func (c *ReviewRequestCron) sendOne(ctx context.Context, cand domain.ReviewRequestCandidate) error {
	subject, html, text, err := BuildReviewRequestEmail(ReviewRequestEmailData{
		SiteURL:  c.SiteURL,
		Name:     firstName(cand.UserName),
		PlanName: cand.PlanName,
		OrderID:  cand.OrderID,
	})
	if err != nil {
		return err
	}
	if err := c.Email.Send(ctx, EmailMessage{
		To:       cand.UserEmail,
		Subject:  subject,
		TextBody: text,
		HTMLBody: html,
	}); err != nil {
		return err
	}
	// Só marca enviado quando o sender devolveu OK — assim retry natural
	// no próximo tick se falhar (rate-limit do Resend, downtime, etc.).
	return c.Repo.MarkReviewEmailSent(ctx, cand.OrderID)
}

func firstName(full string) string {
	for i := 0; i < len(full); i++ {
		if full[i] == ' ' {
			return full[:i]
		}
	}
	if full == "" {
		return "there"
	}
	return full
}
