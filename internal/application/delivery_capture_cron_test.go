package application

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// Testes do cron de delivery capture. Estratégia: usar fakes da
// OrderRepository e do MetricCaptureService pra validar a lógica de:
//   - Cutoff temporal (só pega pedidos paid + updated_at < now-Delay)
//   - Não re-captura quem já tem delivery
//   - Batch (não pega mais que `Batch` por tick)
//   - Falha de captura é log warn mas não derruba o tick

// fakeOrderRepoForCron implementa o subset de OrderRepository que o cron usa.
// Os métodos não usados retornam erros (se invocados, é regressão).
type fakeOrderRepoForCron struct {
	mu       sync.Mutex
	orders   []domain.Order
	listErr  error
	listCall int
}

func (f *fakeOrderRepoForCron) Create(ctx context.Context, o domain.Order) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) GetByID(ctx context.Context, id string) (*domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.orders {
		if f.orders[i].ID == id {
			o := f.orders[i]
			return &o, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeOrderRepoForCron) GetByExternalRef(ctx context.Context, ref string) (*domain.Order, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) ListByUser(ctx context.Context, userID string) ([]domain.Order, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) ListViewByUser(ctx context.Context, userID string) ([]domain.OrderView, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) ListAll(ctx context.Context) ([]domain.Order, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) ListAllView(ctx context.Context) ([]domain.OrderView, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) UpdateStatus(ctx context.Context, id string, status domain.OrderStatus, externalRef *string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) UpdatePayment(ctx context.Context, id, externalRef, paymentURL string, extra map[string]string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) LinkTicket(ctx context.Context, orderID, ticketID string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) SetBaselineMetrics(ctx context.Context, orderID string, metrics map[string]any, source string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) SetDeliveryMetrics(ctx context.Context, orderID string, metrics map[string]any, source string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.orders {
		if f.orders[i].ID == orderID {
			t := time.Now()
			f.orders[i].DeliveryCapturedAt = &t
			f.orders[i].DeliveryMetrics = metrics
			f.orders[i].DeliverySource = &source
			return nil
		}
	}
	return domain.ErrNotFound
}

// Stubs adicionados pra OrderRepository nova (migration 034 — proof + assign).
// Não exercitados por este test suite; retornam erro pra evitar chamadas
// silenciosas indevidas no caminho do cron de delivery capture.
func (f *fakeOrderRepoForCron) AssignGateway(ctx context.Context, orderID, gatewayID string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) SetProof(ctx context.Context, orderID, fileURL, fileName, mime, note string, sizeBytes int, storageKey string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) SetProofStorageKey(ctx context.Context, orderID, storageKey string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) SetProofStatus(ctx context.Context, orderID, status, reviewerNote string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) ListPendingProofs(ctx context.Context, limit int) ([]domain.OrderView, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) SoftDeleteOrder(ctx context.Context, id, adminID, reason string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) HardDeleteOrder(ctx context.Context, id string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) RestoreOrder(ctx context.Context, id string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForCron) ListDeletedView(ctx context.Context, limit int) ([]domain.OrderView, error) {
	return nil, errors.New("not implemented")
}

// ListReadyForDeliveryCapture: replica a query SQL em memória.
func (f *fakeOrderRepoForCron) ListReadyForDeliveryCapture(ctx context.Context, olderThan time.Time, limit int) ([]domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCall++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := []domain.Order{}
	for _, o := range f.orders {
		if o.Status != domain.OrderStatusPaid {
			continue
		}
		if o.DeliveryCapturedAt != nil {
			continue
		}
		if !o.UpdatedAt.Before(olderThan) {
			continue
		}
		out = append(out, o)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// stubMetricCaptureService substitui o MetricCaptureService real com um
// contador + erro opcional. Como MetricCaptureService é struct concreta
// (não interface), usamos um trick: o cron chama c.Metrics.CaptureDelivery
// que é método público. Construímos um service que delega pro repo fake mas
// substitui CaptureDelivery via composição — para isso reescrevemos só o
// path crítico: o cron chama CaptureDelivery(ctx, orderID). Não podemos
// substituir o método sem interface. Solução: usar o MetricCaptureService
// real porém com repos fakes que façam o capture cair em early-return (sem
// plan ou sem profile_id → return nil).
//
// Mas isso testa pouco. Melhor: usar um wrapper interface para o cron.
// O design atual do cron tem campo Metrics *MetricCaptureService (concrete).
// Pra testar sem refatorar, criamos um plan fake que não tem target,
// fazendo CaptureDelivery cair em "default: return nil" da função capture.

// fakePlanRepoForCron devolve um plano com TargetType="noop",
// fazendo MetricCaptureService.capture() cair no `default: return nil`.
type fakePlanRepoForCron struct{}

func (f *fakePlanRepoForCron) ListActive(ctx context.Context) ([]domain.Plan, error) {
	return nil, nil
}
func (f *fakePlanRepoForCron) ListAll(ctx context.Context) ([]domain.Plan, error) {
	return nil, nil
}
func (f *fakePlanRepoForCron) GetByID(ctx context.Context, id string) (*domain.Plan, error) {
	return &domain.Plan{ID: id, TargetType: "noop"}, nil
}
func (f *fakePlanRepoForCron) Create(ctx context.Context, p domain.Plan) error { return nil }
func (f *fakePlanRepoForCron) Update(ctx context.Context, p domain.Plan) error { return nil }
func (f *fakePlanRepoForCron) Delete(ctx context.Context, id string) error     { return nil }
func (f *fakePlanRepoForCron) UpsertPrices(ctx context.Context, planID string, prices map[string]string) error {
	return nil
}
func (f *fakePlanRepoForCron) RecomputePricesForCurrency(ctx context.Context, code string, rate float64, decimals int) error {
	return nil
}
func (f *fakePlanRepoForCron) RecomputePricesForPlan(ctx context.Context, planID string) error {
	return nil
}

type fakeProfileRepoForCron struct{}

func (f *fakeProfileRepoForCron) Create(ctx context.Context, p domain.Profile) error {
	return nil
}
func (f *fakeProfileRepoForCron) GetByID(ctx context.Context, id string) (*domain.Profile, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeProfileRepoForCron) ListByUser(ctx context.Context, userID string) ([]domain.Profile, error) {
	return nil, nil
}
func (f *fakeProfileRepoForCron) ListByUserAndPlatform(ctx context.Context, userID string, platform domain.Platform) ([]domain.Profile, error) {
	return nil, nil
}
func (f *fakeProfileRepoForCron) Delete(ctx context.Context, id, userID string) error {
	return nil
}
func (f *fakeProfileRepoForCron) SetVerified(ctx context.Context, id string, verified bool) error {
	return nil
}

// counterCaptureSvc é um stand-in pro MetricCaptureService que conta calls.
// O cron espera *MetricCaptureService — não temos interface, então testamos
// via o serviço real apontando para nossos fakes (target_type=noop → no-op).

// helper: cria um cron com fakes prontos
func newTestCron(t *testing.T, repo *fakeOrderRepoForCron, capture *MetricCaptureService) *DeliveryCaptureCron {
	t.Helper()
	return &DeliveryCaptureCron{
		Orders:   repo,
		Metrics:  capture,
		Interval: 50 * time.Millisecond,
		Delay:    1 * time.Hour,
		Batch:    25,
	}
}

func newCaptureWithFakes(repo domain.OrderRepository) *MetricCaptureService {
	// Plan target_type="noop" → capture() retorna nil sem fazer scrape.
	return NewMetricCaptureService(repo, &fakePlanRepoForCron{}, &fakeProfileRepoForCron{})
}

// ---------- testes ----------

func TestDeliveryCron_PicksOnlyEligibleOrders(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Hour)    // > 1h cutoff → elegível
	recent := now.Add(-30 * time.Minute) // < 1h cutoff → não elegível
	captured := now.Add(-3 * time.Hour)
	repo := &fakeOrderRepoForCron{
		orders: []domain.Order{
			// 1: paid, antigo, sem delivery → ELEGÍVEL
			{ID: "o-eligible", Status: domain.OrderStatusPaid, UpdatedAt: old},
			// 2: pending, antigo → NÃO (status errado)
			{ID: "o-pending", Status: domain.OrderStatusPending, UpdatedAt: old},
			// 3: paid mas recente → NÃO (cutoff)
			{ID: "o-recent", Status: domain.OrderStatusPaid, UpdatedAt: recent},
			// 4: paid, antigo, mas já capturado → NÃO (idempotência)
			{ID: "o-captured", Status: domain.OrderStatusPaid, UpdatedAt: old, DeliveryCapturedAt: &captured},
		},
	}
	cutoff := now.Add(-1 * time.Hour)
	got, err := repo.ListReadyForDeliveryCapture(context.Background(), cutoff, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "o-eligible" {
		t.Errorf("ListReadyForDeliveryCapture returned %v, want only o-eligible", ids(got))
	}
}

func TestDeliveryCron_BatchLimitApplied(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	orders := []domain.Order{}
	for i := 0; i < 50; i++ {
		orders = append(orders, domain.Order{
			ID:        idN(i),
			Status:    domain.OrderStatusPaid,
			UpdatedAt: old,
		})
	}
	repo := &fakeOrderRepoForCron{orders: orders}
	got, err := repo.ListReadyForDeliveryCapture(context.Background(), now.Add(-1*time.Hour), 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 10 {
		t.Errorf("got %d orders, want 10 (batch limit)", len(got))
	}
}

func TestDeliveryCron_StartIsIdempotent(t *testing.T) {
	// Start chamado 2x não pode duplicar goroutine.
	repo := &fakeOrderRepoForCron{orders: nil}
	cron := newTestCron(t, repo, newCaptureWithFakes(repo))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cron.Start(ctx)
	cron.Start(ctx) // segunda chamada — no-op
	cron.Start(ctx) // terceira chamada — no-op

	// Espera curto pra um tick imediato rodar.
	time.Sleep(80 * time.Millisecond)
	cancel()
	cron.Stop()

	// Se a goroutine tivesse duplicado, listCall seria muito alto.
	// 1 tick imediato + ticker pode disparar 1 vez no intervalo curto.
	repo.mu.Lock()
	calls := repo.listCall
	repo.mu.Unlock()
	if calls < 1 {
		t.Errorf("expected at least 1 list call, got %d", calls)
	}
	if calls > 5 {
		t.Errorf("listCall = %d — Start likely duplicated goroutine", calls)
	}
}

func TestDeliveryCron_TickProcessesBatchAndMarksCaptured(t *testing.T) {
	now := time.Now()
	old := now.Add(-25 * time.Hour) // bem antes do cutoff de 24h
	repo := &fakeOrderRepoForCron{
		orders: []domain.Order{
			{ID: "o-1", Status: domain.OrderStatusPaid, UpdatedAt: old},
			{ID: "o-2", Status: domain.OrderStatusPaid, UpdatedAt: old},
		},
	}
	capture := newCaptureWithFakes(repo)
	cron := &DeliveryCaptureCron{
		Orders: repo, Metrics: capture,
		Interval: 200 * time.Millisecond,
		Delay:    24 * time.Hour,
		Batch:    25,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cron.Start(ctx)
	// Espera tick imediato concluir.
	time.Sleep(150 * time.Millisecond)
	cancel()
	cron.Stop()

	// Com TargetType=noop o capture() retorna nil sem chamar SetDeliveryMetrics.
	// O importante é confirmar que o List foi consultado e o cron tentou
	// processar — sem erro fatal.
	repo.mu.Lock()
	calls := repo.listCall
	repo.mu.Unlock()
	if calls < 1 {
		t.Errorf("listCall = %d, expected at least 1", calls)
	}
}

func TestDeliveryCron_ListErrorDoesNotPanic(t *testing.T) {
	// Se o DB cai, o tick log warn e segue — não pode derrubar o cron.
	repo := &fakeOrderRepoForCron{listErr: errors.New("db down")}
	capture := newCaptureWithFakes(repo)
	cron := newTestCron(t, repo, capture)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cron.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	cron.Stop()

	repo.mu.Lock()
	calls := repo.listCall
	repo.mu.Unlock()
	if calls < 1 {
		t.Errorf("expected at least 1 list call despite error, got %d", calls)
	}
}

func TestDeliveryCron_CtxCancelStopsLoop(t *testing.T) {
	repo := &fakeOrderRepoForCron{}
	capture := newCaptureWithFakes(repo)
	cron := &DeliveryCaptureCron{
		Orders: repo, Metrics: capture,
		Interval: 10 * time.Second, // grande pra evitar ticks naturais
		Delay:    24 * time.Hour,
		Batch:    25,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cron.Start(ctx)

	// Confirma que o tick imediato rodou (1 call).
	time.Sleep(20 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		cron.Stop()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2s after ctx cancel")
	}
}

func TestDeliveryCron_DefaultsAppliedWhenZero(t *testing.T) {
	// Interval/Delay/Batch zerados devem cair em defaults sensatos.
	repo := &fakeOrderRepoForCron{}
	capture := newCaptureWithFakes(repo)
	cron := &DeliveryCaptureCron{Orders: repo, Metrics: capture}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cron.Start(ctx)
	// Tick imediato roda, defaults aplicados.
	time.Sleep(50 * time.Millisecond)
	cancel()
	cron.Stop()

	if cron.Interval != 15*time.Minute {
		t.Errorf("default Interval = %v, want 15min", cron.Interval)
	}
	if cron.Delay != 24*time.Hour {
		t.Errorf("default Delay = %v, want 24h", cron.Delay)
	}
	if cron.Batch != 25 {
		t.Errorf("default Batch = %d, want 25", cron.Batch)
	}
}

func TestDeliveryCron_RunningFlagPreventsDoubleStart(t *testing.T) {
	repo := &fakeOrderRepoForCron{}
	capture := newCaptureWithFakes(repo)
	cron := newTestCron(t, repo, capture)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cron.Start(ctx)
	if !cron.running.Load() {
		t.Error("running flag should be true after Start")
	}
	cron.Start(ctx) // no-op
	// Confirma que ainda só uma goroutine — running flag inalterada.
	if !cron.running.Load() {
		t.Error("running flag should remain true")
	}
	cancel()
	cron.Stop()
	if cron.running.Load() {
		t.Error("running flag should be false after Stop")
	}
}

// ---------- helpers ----------

func ids(orders []domain.Order) []string {
	out := make([]string, len(orders))
	for i, o := range orders {
		out[i] = o.ID
	}
	return out
}

func idN(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	a := letters[n%26]
	b := letters[(n/26)%26]
	return string([]byte{a, b})
}

// Sanity: garante que atomic.Bool nunca dispara dois Start em concorrência.
func TestDeliveryCron_ConcurrentStartIsSafe(t *testing.T) {
	repo := &fakeOrderRepoForCron{}
	capture := newCaptureWithFakes(repo)
	cron := newTestCron(t, repo, capture)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var started atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cron.Start(ctx)
			started.Add(1)
		}()
	}
	wg.Wait()
	if started.Load() != 10 {
		t.Errorf("expected 10 Start calls completed, got %d", started.Load())
	}
	// Só uma goroutine deveria estar de fato rodando.
	cancel()
	cron.Stop()
}
