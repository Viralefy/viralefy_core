package application

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// IdempotencyCleanupCron remove rows expiradas de idempotency_keys.
//
// Por que existe: o middleware insere uma row por mutation com header
// Idempotency-Key (checkout, recovery, review submit). TTL é 24h via
// `expires_at = NOW() + INTERVAL '24 hours'`. Sem cleanup a tabela cresce
// indefinidamente — comentário em internal/interface/http/idempotency.go
// marcava como tech debt ("implementar cron de cleanup; por enquanto a
// tabela cresce"). Resolvido aqui.
//
// Política:
//   - Tick a cada `Interval` (padrão 1h — não é tempo-crítico).
//   - DELETE com LIMIT pra evitar lock longo em tabela grande. Tick
//     repetido até a deleção retornar 0.
//   - Erros são log warn (DB pode estar momentaneamente saturado).
type IdempotencyCleanupCron struct {
	DB       *postgres.DB
	Interval time.Duration // default 1h

	running atomic.Bool
	stopped chan struct{}
}

func (c *IdempotencyCleanupCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = 1 * time.Hour
	}
	c.stopped = make(chan struct{})
	go c.loop(ctx)
}

func (c *IdempotencyCleanupCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *IdempotencyCleanupCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "idempotency_cleanup")
	logger.Info("idempotency cleanup cron started", "interval", c.Interval.String())

	c.tick(ctx)

	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("idempotency cleanup cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

func (c *IdempotencyCleanupCron) tick(ctx context.Context) {
	logger := observability.FromContext(ctx).With("cron", "idempotency_cleanup")
	tickCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Loop com LIMIT pra não segurar lock longo. Postgres não tem
	// LIMIT no DELETE direto, usamos CTE com SELECT FOR UPDATE SKIP LOCKED.
	var totalDeleted int64
	for {
		tag, err := c.DB.Pool().Exec(tickCtx, `
			WITH due AS (
				SELECT key FROM idempotency_keys
				WHERE expires_at < NOW()
				ORDER BY expires_at ASC
				LIMIT 500
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM idempotency_keys
			WHERE key IN (SELECT key FROM due)`)
		if err != nil {
			logger.Warn("delete batch failed", "error", err.Error())
			return
		}
		n := tag.RowsAffected()
		totalDeleted += n
		if n < 500 {
			break
		}
	}
	if totalDeleted > 0 {
		logger.Info("expired keys removed", "deleted", totalDeleted)
	}
}
