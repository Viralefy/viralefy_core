package application

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// SubscriptionCron tickando a cada hora chama
// SubscriptionService.ProcessDueRenewals. 1h é granular o bastante pro
// MVP (subs vencem em granularidade de "dia"; 23h de atraso máximo na
// renovação é tolerável). Mais agressivo desperdiça CPU; menos arrisca
// renovações atrasadas demais.
type SubscriptionCron struct {
	svc      *SubscriptionService
	Interval time.Duration

	running atomic.Bool
	stopped chan struct{}
}

func NewSubscriptionCron(svc *SubscriptionService) *SubscriptionCron {
	return &SubscriptionCron{svc: svc}
}

func (c *SubscriptionCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = time.Hour
	}
	c.stopped = make(chan struct{})
	go c.loop(ctx)
}

func (c *SubscriptionCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *SubscriptionCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "subscriptions")
	logger.Info("subscription cron started", "interval", c.Interval.String())

	c.tick(ctx)

	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("subscription cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

func (c *SubscriptionCron) tick(ctx context.Context) {
	logger := observability.FromContext(ctx).With("cron", "subscriptions")
	tickCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := c.svc.ProcessDueRenewals(tickCtx); err != nil {
		logger.Warn("process due renewals failed", "error", err.Error())
	}
}
