package application

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// DeliveryCaptureCron roda em background tirando snapshots de delivery dos
// pedidos pagos há pelo menos `Delay` (padrão 24h) que ainda não tiveram a
// 2ª fonte de verdade capturada.
//
// Antes desse cron, delivery era 100% manual: admin precisava abrir cada
// /orders/{id} e clicar "Capturar delivery agora". Inviável em escala.
//
// Política:
//   - Roda a cada `Interval` (padrão 15min).
//   - Pega lote pequeno (`Batch`, padrão 25) e processa serial — não vale a
//     pena paralelizar; scraper já tem timeout de 10s por captura e gera
//     load no Instagram/TikTok (rate-limit do alvo).
//   - Failures por order não derrubam o tick — log warn e segue.
//   - Cron é idempotente: se um order já tem delivery_captured_at != NULL,
//     o ListReadyForDeliveryCapture não o devolve mais.
type DeliveryCaptureCron struct {
	Orders   domain.OrderRepository
	Metrics  *MetricCaptureService
	Interval time.Duration // entre ticks; default 15min
	Delay    time.Duration // mínimo desde paid; default 24h
	Batch    int           // máximo por tick; default 25

	running atomic.Bool
	stopped chan struct{}
}

// Start dispara a goroutine. Cancelar via ctx ou chamar Stop().
// No-op se já estiver rodando (idempotente em hot reload de config futuro).
func (c *DeliveryCaptureCron) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	if c.Interval <= 0 {
		c.Interval = 15 * time.Minute
	}
	if c.Delay <= 0 {
		c.Delay = 24 * time.Hour
	}
	if c.Batch <= 0 {
		c.Batch = 25
	}
	c.stopped = make(chan struct{})

	go c.loop(ctx)
}

// Stop sinaliza parada e espera o último tick concluir. Idempotente.
func (c *DeliveryCaptureCron) Stop() {
	if !c.running.Load() {
		return
	}
	<-c.stopped
}

func (c *DeliveryCaptureCron) loop(ctx context.Context) {
	defer close(c.stopped)
	defer c.running.Store(false)

	logger := observability.FromContext(ctx).With("cron", "delivery_capture")
	logger.Info("delivery capture cron started",
		"interval", c.Interval.String(),
		"delay", c.Delay.String(),
		"batch", c.Batch,
	)

	// Tick imediato no boot — captura qualquer fila atrasada de downtime.
	c.tick(ctx, logger)

	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("delivery capture cron stopped (ctx done)")
			return
		case <-t.C:
			c.tick(ctx, logger)
		}
	}
}

// tick faz uma rodada: consulta DB, processa lote serial. Erros no scrape
// são logados warn e a iteração continua — falhar uma captura não pode
// invalidar as outras do batch.
func (c *DeliveryCaptureCron) tick(ctx context.Context, logger *slog.Logger) {
	cutoff := time.Now().Add(-c.Delay)
	tickCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	orders, err := c.Orders.ListReadyForDeliveryCapture(tickCtx, cutoff, c.Batch)
	if err != nil {
		logger.Warn("list ready failed", "error", err.Error())
		return
	}
	if len(orders) == 0 {
		return
	}
	logger.Info("processing batch", "count", len(orders), "cutoff", cutoff.Format(time.RFC3339))

	var ok, failed int
	for _, o := range orders {
		// Cada captura tem seu próprio ctx pra não ser cancelado pelos outros.
		// MetricCaptureService.capture() já tem timeout interno de 20s.
		captureCtx, cancelCapture := context.WithTimeout(ctx, 30*time.Second)
		if err := c.Metrics.CaptureDelivery(captureCtx, o.ID); err != nil {
			failed++
			logger.Warn("delivery capture failed",
				"order_id", o.ID,
				"error", err.Error(),
			)
		} else {
			ok++
		}
		cancelCapture()
	}
	logger.Info("batch done", "ok", ok, "failed", failed)
}
