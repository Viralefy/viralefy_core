package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// FraudVelocityCron varre orders/login attempts agregados a cada `Interval`
// (default 5min) e grava sinais históricos. Diferente do CheckEmail/CheckIP
// que são síncronos no fluxo de checkout/login, este cron alimenta o
// dashboard admin com snapshots periódicos pra detectar picos que escapam
// das checagens online (ex: ataque que distribui carga entre vários IPs
// dentro do mesmo /24).
//
// Política:
//   - Tick 5min. Granular o suficiente pra pegar picos curtos sem flood.
//   - Idempotência best-effort: gravamos uma row por tick e por bucket
//     (email/IP) — se o cron roda duas vezes próximo, ficam dois sinais
//     com triggered_at distintos; dashboard agrupa por janela.
//   - Cleanup automático: dropa sinais com mais de 30 dias. Mantém o
//     histórico curto pra não inflar a tabela.
type FraudVelocityCron struct {
	DB       *postgres.DB
	Interval time.Duration

	running atomic.Bool
	stopped chan struct{}
}

func NewFraudVelocityCron(db *postgres.DB) *FraudVelocityCron {
	return &FraudVelocityCron{DB: db}
}

func (c *FraudVelocityCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	c.stopped = make(chan struct{})
	go c.loop(ctx)
}

func (c *FraudVelocityCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *FraudVelocityCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "fraud_velocity")
	logger.Info("fraud velocity cron started", "interval", c.Interval.String())

	c.tick(ctx)

	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("fraud velocity cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

func (c *FraudVelocityCron) tick(ctx context.Context) {
	logger := observability.FromContext(ctx).With("cron", "fraud_velocity")
	tickCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	c.scanEmailVelocity(tickCtx, logger)
	c.cleanup(tickCtx, logger)
}

func (c *FraudVelocityCron) scanEmailVelocity(ctx context.Context, logger *slog.Logger) {
	rows, err := c.DB.Pool().Query(ctx, `
		SELECT LOWER(u.email) AS email, COUNT(*) AS n
		FROM orders o
		JOIN users u ON u.id = o.user_id
		WHERE o.created_at > NOW() - INTERVAL '24 hours'
		GROUP BY LOWER(u.email)
		HAVING COUNT(*) >= $1`, fraudEmailWarnThreshold)
	if err != nil {
		logger.Warn("scan email velocity failed", "error", err.Error())
		return
	}
	type bucket struct {
		email string
		n     int
	}
	var buckets []bucket
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.email, &b.n); err != nil {
			logger.Warn("scan email bucket failed", "error", err.Error())
			continue
		}
		buckets = append(buckets, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		logger.Warn("scan email rows iteration failed", "error", err.Error())
		return
	}
	for _, b := range buckets {
		severity := "warn"
		if b.n >= fraudEmailBlockThreshold {
			severity = "block"
		}
		detail, _ := json.Marshal(map[string]any{
			"count":  b.n,
			"window": "24h",
			"source": "cron",
		})
		_, err := c.DB.Pool().Exec(ctx, `
			INSERT INTO fraud_signals (id, signal_type, actor, severity, detail)
			VALUES ($1, $2, $3, $4, $5)`,
			uuid.New().String(), "email_velocity", b.email, severity, detail,
		)
		if err != nil {
			logger.Warn("insert email signal failed", "email", b.email, "error", err.Error())
		}
	}
	if len(buckets) > 0 {
		logger.Info("email velocity scan done", "buckets", len(buckets))
	}
}

func (c *FraudVelocityCron) cleanup(ctx context.Context, logger *slog.Logger) {
	tag, err := c.DB.Pool().Exec(ctx,
		`DELETE FROM fraud_signals WHERE triggered_at < NOW() - INTERVAL '30 days'`)
	if err != nil {
		logger.Warn("cleanup fraud signals failed", "error", err.Error())
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		logger.Info("old signals removed", "deleted", n)
	}
	// Blocks expirados também são limpos — sem cleanup ficam visíveis no
	// dashboard como "ativos" pelo PK lookup, mas IsBlocked já filtra por
	// blocked_until.
	tag, err = c.DB.Pool().Exec(ctx,
		`DELETE FROM fraud_blocks WHERE blocked_until < NOW() - INTERVAL '7 days'`)
	if err != nil {
		logger.Warn("cleanup fraud blocks failed", "error", err.Error())
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		logger.Info("old blocks removed", "deleted", n)
	}
}
