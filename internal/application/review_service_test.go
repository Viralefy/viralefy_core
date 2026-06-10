package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// fakeReviewRepo + reuso de fakePlanRepoForCron + um fakeOrderRepo simples
// que devolve um order conforme o test setup.

type fakeReviewRepo struct {
	created    []domain.Review
	visibility map[string]bool
	existing   map[string]domain.Review // por order_id
	aggByPlan  map[string]*domain.AggregateRating
	aggByCat   map[string]*domain.AggregateRating
}

func newFakeReviewRepo() *fakeReviewRepo {
	return &fakeReviewRepo{
		visibility: map[string]bool{},
		existing:   map[string]domain.Review{},
		aggByPlan:  map[string]*domain.AggregateRating{},
		aggByCat:   map[string]*domain.AggregateRating{},
	}
}

func (f *fakeReviewRepo) Create(ctx context.Context, r domain.Review) error {
	if _, ok := f.existing[r.OrderID]; ok {
		return domain.ErrConflict
	}
	f.existing[r.OrderID] = r
	f.created = append(f.created, r)
	return nil
}
func (f *fakeReviewRepo) GetByOrderID(ctx context.Context, orderID string) (*domain.Review, error) {
	r, ok := f.existing[orderID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &r, nil
}
func (f *fakeReviewRepo) ListPublicByPlan(ctx context.Context, planID string, limit int) ([]domain.PublicReview, error) {
	return nil, nil
}
func (f *fakeReviewRepo) ListPublicByCategory(ctx context.Context, cat string, limit int) ([]domain.PublicReview, error) {
	return nil, nil
}
func (f *fakeReviewRepo) AggregateByPlan(ctx context.Context, planID string) (*domain.AggregateRating, error) {
	return f.aggByPlan[planID], nil
}
func (f *fakeReviewRepo) AggregateByCategory(ctx context.Context, cat string) (*domain.AggregateRating, error) {
	return f.aggByCat[cat], nil
}
func (f *fakeReviewRepo) SetVisibility(ctx context.Context, id string, visible bool) error {
	f.visibility[id] = visible
	return nil
}
func (f *fakeReviewRepo) ListAdmin(ctx context.Context, filter domain.AdminReviewFilter, limit int) ([]domain.AdminReview, error) {
	return nil, nil
}
func (f *fakeReviewRepo) GetByID(ctx context.Context, id string) (*domain.Review, error) {
	for _, r := range f.existing {
		if r.ID == id {
			r := r
			return &r, nil
		}
	}
	return nil, domain.ErrNotFound
}

// fakeOrderRepoForReview: implementa só o que ReviewService.Create chama
// (GetByID). Os outros métodos devolvem erro pra forçar regressão visível.
type fakeOrderRepoForReview struct {
	orders map[string]domain.Order
}

func newFakeOrderRepoForReview() *fakeOrderRepoForReview {
	return &fakeOrderRepoForReview{orders: map[string]domain.Order{}}
}
func (f *fakeOrderRepoForReview) put(o domain.Order) { f.orders[o.ID] = o }

func (f *fakeOrderRepoForReview) Create(ctx context.Context, o domain.Order) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) GetByID(ctx context.Context, id string) (*domain.Order, error) {
	o, ok := f.orders[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &o, nil
}
func (f *fakeOrderRepoForReview) GetByExternalRef(ctx context.Context, ref string) (*domain.Order, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) ListByUser(ctx context.Context, userID string) ([]domain.Order, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) ListViewByUser(ctx context.Context, userID string) ([]domain.OrderView, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) ListAll(ctx context.Context) ([]domain.Order, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) ListAllView(ctx context.Context) ([]domain.OrderView, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) UpdateStatus(ctx context.Context, id string, status domain.OrderStatus, externalRef *string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) UpdatePayment(ctx context.Context, id, externalRef, paymentURL string, extra map[string]string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) LinkTicket(ctx context.Context, orderID, ticketID string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) SetBaselineMetrics(ctx context.Context, orderID string, metrics map[string]any, source string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) SetDeliveryMetrics(ctx context.Context, orderID string, metrics map[string]any, source string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) ListReadyForDeliveryCapture(ctx context.Context, olderThan time.Time, limit int) ([]domain.Order, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) AssignGateway(ctx context.Context, orderID, gatewayID string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) SetProof(ctx context.Context, orderID, fileURL, fileName, mime, note string, sizeBytes int, storageKey string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) SetProofStorageKey(ctx context.Context, orderID, storageKey string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) SetProofStatus(ctx context.Context, orderID, status, reviewerNote string) error {
	return errors.New("not implemented")
}
func (f *fakeOrderRepoForReview) ListPendingProofs(ctx context.Context, limit int) ([]domain.OrderView, error) {
	return nil, errors.New("not implemented")
}

// fakePlanRepoForReview retorna um plan fixo por id.
type fakePlanRepoForReview struct{}

func (f *fakePlanRepoForReview) ListActive(ctx context.Context) ([]domain.Plan, error) {
	return nil, nil
}
func (f *fakePlanRepoForReview) ListAll(ctx context.Context) ([]domain.Plan, error) {
	return nil, nil
}
func (f *fakePlanRepoForReview) GetByID(ctx context.Context, id string) (*domain.Plan, error) {
	return &domain.Plan{ID: id, Category: "seguidores_instagram"}, nil
}
func (f *fakePlanRepoForReview) Create(ctx context.Context, p domain.Plan) error { return nil }
func (f *fakePlanRepoForReview) Update(ctx context.Context, p domain.Plan) error { return nil }
func (f *fakePlanRepoForReview) Delete(ctx context.Context, id string) error     { return nil }
func (f *fakePlanRepoForReview) UpsertPrices(ctx context.Context, planID string, prices map[string]string) error {
	return nil
}
func (f *fakePlanRepoForReview) RecomputePricesForCurrency(ctx context.Context, code string, rate float64, decimals int) error {
	return nil
}
func (f *fakePlanRepoForReview) RecomputePricesForPlan(ctx context.Context, planID string) error {
	return nil
}

// ---------- testes ----------

func newReviewSetup(t *testing.T) (*ReviewService, *fakeOrderRepoForReview, *fakeReviewRepo) {
	t.Helper()
	revRepo := newFakeReviewRepo()
	ordRepo := newFakeOrderRepoForReview()
	planRepo := &fakePlanRepoForReview{}
	svc := NewReviewService(revRepo, ordRepo, planRepo)
	return svc, ordRepo, revRepo
}

func TestReviewService_AcceptsValidPaidOrder(t *testing.T) {
	svc, ordRepo, _ := newReviewSetup(t)
	ordRepo.put(domain.Order{ID: "o-1", UserID: "u-1", PlanID: "p-1", Status: domain.OrderStatusPaid})

	rev, err := svc.Create(context.Background(), CreateReviewInput{
		UserID: "u-1", OrderID: "o-1", Rating: 5, Title: "Loved it", Body: "Fast & clean", CountryCode: "BR",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rev.Rating != 5 || rev.Title != "Loved it" {
		t.Errorf("review fields lost: %+v", rev)
	}
	if rev.CountryCode != "br" {
		t.Errorf("country_code = %q, want lowercased 'br'", rev.CountryCode)
	}
	if !rev.Visible {
		t.Errorf("review must default to visible=true")
	}
	if rev.PlanCategory != "seguidores_instagram" {
		t.Errorf("plan_category not hydrated from plan: %q", rev.PlanCategory)
	}
}

func TestReviewService_RejectsRatingOutOfRange(t *testing.T) {
	svc, ordRepo, _ := newReviewSetup(t)
	ordRepo.put(domain.Order{ID: "o-1", UserID: "u-1", PlanID: "p-1", Status: domain.OrderStatusPaid})

	for _, bad := range []int{0, -1, 6, 100} {
		_, err := svc.Create(context.Background(), CreateReviewInput{
			UserID: "u-1", OrderID: "o-1", Rating: bad,
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("rating=%d should be rejected, got %v", bad, err)
		}
	}
}

func TestReviewService_RejectsUnpaidOrder(t *testing.T) {
	svc, ordRepo, _ := newReviewSetup(t)
	for _, status := range []domain.OrderStatus{
		domain.OrderStatusPending,
		domain.OrderStatusFailed,
		domain.OrderStatusCancelled,
	} {
		ordRepo.put(domain.Order{ID: "o-1", UserID: "u-1", PlanID: "p-1", Status: status})
		_, err := svc.Create(context.Background(), CreateReviewInput{
			UserID: "u-1", OrderID: "o-1", Rating: 4,
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("status=%s should reject review (delivery not confirmed), got %v", status, err)
		}
	}
}

func TestReviewService_RejectsForeignOrderOwnership(t *testing.T) {
	svc, ordRepo, _ := newReviewSetup(t)
	ordRepo.put(domain.Order{ID: "o-1", UserID: "u-OWNER", PlanID: "p-1", Status: domain.OrderStatusPaid})

	_, err := svc.Create(context.Background(), CreateReviewInput{
		UserID: "u-NOT-OWNER", OrderID: "o-1", Rating: 5,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("non-owner must get 403, got %v", err)
	}
}

func TestReviewService_RejectsDuplicate(t *testing.T) {
	svc, ordRepo, _ := newReviewSetup(t)
	ordRepo.put(domain.Order{ID: "o-1", UserID: "u-1", PlanID: "p-1", Status: domain.OrderStatusPaid})

	_, err := svc.Create(context.Background(), CreateReviewInput{UserID: "u-1", OrderID: "o-1", Rating: 5})
	if err != nil {
		t.Fatalf("first review failed: %v", err)
	}
	_, err = svc.Create(context.Background(), CreateReviewInput{UserID: "u-1", OrderID: "o-1", Rating: 4})
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second review must conflict, got %v", err)
	}
}

func TestReviewService_TrimsAndTruncatesText(t *testing.T) {
	svc, ordRepo, _ := newReviewSetup(t)
	ordRepo.put(domain.Order{ID: "o-1", UserID: "u-1", PlanID: "p-1", Status: domain.OrderStatusPaid})

	longTitle := makeStr(200)
	longBody := makeStr(3000)
	rev, err := svc.Create(context.Background(), CreateReviewInput{
		UserID: "u-1", OrderID: "o-1", Rating: 4,
		Title: "   spaces around   ",
		Body:  longBody,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rev.Title != "spaces around" {
		t.Errorf("title not trimmed: %q", rev.Title)
	}
	if len(rev.Body) != 2000 {
		t.Errorf("body length = %d, want 2000 (truncated)", len(rev.Body))
	}

	// re-roda com title > 120 (existing já tem 1 review, vamos limpar e repetir)
	rev2, err := svc.Create(context.Background(), CreateReviewInput{
		UserID: "u-2", OrderID: "o-2", Rating: 4, Title: longTitle,
	})
	if err == nil && len(rev2.Title) > 120 {
		t.Errorf("title length = %d, want <=120", len(rev2.Title))
	}
}

func TestReviewService_RejectsMissingIDs(t *testing.T) {
	svc, _, _ := newReviewSetup(t)
	for _, in := range []CreateReviewInput{
		{UserID: "", OrderID: "o-1", Rating: 5},
		{UserID: "u-1", OrderID: "", Rating: 5},
		{UserID: "", OrderID: "", Rating: 5},
	} {
		_, err := svc.Create(context.Background(), in)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("input %+v should be rejected, got %v", in, err)
		}
	}
}

func TestReviewService_AggregateByPlanReturnsRepoValue(t *testing.T) {
	svc, _, revRepo := newReviewSetup(t)
	revRepo.aggByPlan["p-1"] = &domain.AggregateRating{
		RatingValue: 4.5, ReviewCount: 12, BestRating: 5, WorstRating: 1,
	}
	got, err := svc.AggregateByPlan(context.Background(), "p-1")
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got == nil || got.RatingValue != 4.5 || got.ReviewCount != 12 {
		t.Errorf("aggregate = %+v, want 4.5/12", got)
	}
}

func TestReviewService_AggregateByPlanNilWhenNoReviews(t *testing.T) {
	svc, _, _ := newReviewSetup(t)
	got, err := svc.AggregateByPlan(context.Background(), "p-empty")
	if err != nil || got != nil {
		t.Errorf("empty plan should return (nil, nil), got (%+v, %v)", got, err)
	}
}

func TestReviewService_CountryCodeDefaultsToUSWhenEmpty(t *testing.T) {
	svc, ordRepo, _ := newReviewSetup(t)
	ordRepo.put(domain.Order{ID: "o-1", UserID: "u-1", PlanID: "p-1", Status: domain.OrderStatusPaid})

	rev, err := svc.Create(context.Background(), CreateReviewInput{
		UserID: "u-1", OrderID: "o-1", Rating: 5, CountryCode: "",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rev.CountryCode != "us" {
		t.Errorf("empty country_code should fallback to 'us', got %q", rev.CountryCode)
	}
}

// helpers
func makeStr(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
