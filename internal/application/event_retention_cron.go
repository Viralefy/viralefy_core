package application

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// EventRetentionCron remove rows antigas de tabelas append-only de eventos:
//
//   - user_events       — tracking comportamental granular (pageview, click,
//                         modal, checkout). Cresce ~1 row/visitante/click.
//   - ab_events         — exposições + conversões do A/B test harness.
//   - email_events      — webhook hooks da Resend (delivered/bounced/spam).
//
// O agregado de jornada (user_journeys, com total_events) já está em uma
// tabela separada e NÃO é tocado — esse agregado é load-bearing pro
// remarketing e retém valor indefinidamente.
//
// Política:
//   - Tick a cada `Interval` (padrão 24h — eventos antigos não precisam de
//     atenção horária).
//   - Default cutoff: 90 dias. Suficiente pro look-back de attribution Meta
//     CAPI (28d) e funnel/cohort analysis (até 90d). Configurável por env.
//   - DELETE em batches LIMIT pra evitar lock longo (tabelas crescem fácil
//     pra centenas de milhares de rows).
//   - Erros são log warn (cron volta a tentar no próximo tick).
type EventRetentionCron struct {
	DB *postgres.DB
	// Interval entre ticks. Default 24h. Não usar valores menores que 1h
	// — DELETE em alta cardinalidade é trabalho pesado e não precisa
	// rodar com frequência.
	Interval time.Duration
	// MaxAge define o cutoff: rows com occurred_at/created_at < NOW() -
	// MaxAge são apagadas. Default 90 dias.
	MaxAge time.Duration

	running atomic.Bool
	stopped chan struct{}
}

func (c *EventRetentionCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = 24 * time.Hour
	}
	if c.MaxAge <= 0 {
		c.MaxAge = 90 * 24 * time.Hour
	}
	c.stopped = make(chan struct{})
	go c.loop(ctx)
}

func (c *EventRetentionCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *EventRetentionCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "event_retention")
	logger.Info("event retention cron started",
		"interval", c.Interval.String(),
		"max_age", c.MaxAge.String(),
	)
	c.tick(ctx)

	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("event retention cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

// retentionTarget descreve UMA tabela que o cron limpa. Centraliza pra
// adicionar novas tabelas sem repetir o loop de batch DELETE.
type retentionTarget struct {
	// table — nome físico da tabela (sem schema; mesmo db).
	table string
	// timeColumn — coluna usada pra cutoff (geralmente occurred_at ou
	// created_at).
	timeColumn string
}

// targets é a lista de tabelas que o cron de retenção limpa. Adicionar
// nova tabela aqui = adicionar à lista; resto da lógica não muda.
var eventRetentionTargets = []retentionTarget{
	{table: "user_events", timeColumn: "occurred_at"},
	// ab_events usa occurred_at (não created_at — schema antigo).
	{table: "ab_events", timeColumn: "occurred_at"},
	// email_events usa received_at (webhook hooks da Resend).
	{table: "email_events", timeColumn: "received_at"},
	{table: "stripe_events_processed", timeColumn: "received_at"},
}

func (c *EventRetentionCron) tick(ctx context.Context) {
	logger := observability.FromContext(ctx).With("cron", "event_retention")
	tickCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	for _, tgt := range eventRetentionTargets {
		c.cleanupTable(tickCtx, logger, tgt)
	}
}

func (c *EventRetentionCron) cleanupTable(ctx context.Context, logger *slog.Logger, tgt retentionTarget) {
	// Loop com LIMIT pra não segurar lock longo. Tabelas append-only podem
	// ter milhões de rows; deletar tudo numa transação iria estourar WAL.
	var totalDeleted int64
	for {
		// Não há LIMIT no DELETE de Postgres direto — usamos CTE com
		// SELECT FOR UPDATE SKIP LOCKED. ctid é o "row identifier" físico,
		// estável dentro da transação e mais barato que PK pra DELETE em
		// massa. SKIP LOCKED previne stall se outra transação estiver
		// segurando rows (raro pra tabelas append-only mas defensivo).
		// make_interval evita problema de cast '90'::INTERVAL não-parseável.
		query := `
			WITH due AS (
				SELECT ctid FROM ` + tgt.table + `
				WHERE ` + tgt.timeColumn + ` < NOW() - make_interval(secs => $1)
				ORDER BY ` + tgt.timeColumn + ` ASC
				LIMIT 1000
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM ` + tgt.table + `
			WHERE ctid IN (SELECT ctid FROM due)`
		intervalSec := c.MaxAge.Seconds()
		tag, err := c.DB.Pool().Exec(ctx, query, intervalSec)
		if err != nil {
			logger.Warn("delete batch failed",
				"table", tgt.table,
				"error", err.Error(),
			)
			return
		}
		n := tag.RowsAffected()
		totalDeleted += n
		if n < 1000 {
			break
		}
	}
	if totalDeleted > 0 {
		logger.Info("retention cleanup",
			"table", tgt.table,
			"deleted", totalDeleted,
			"max_age", c.MaxAge.String(),
		)
	}
}
