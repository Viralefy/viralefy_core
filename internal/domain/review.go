package domain

import (
	"context"
	"time"
)

// Review é o feedback do cliente sobre um pedido entregue. Um por order
// (UNIQUE no DB) — sem fabricação ou duplicação. Visível por padrão; admin
// pode despublicar via Visible=false (caso content abuse).
//
// Reviews alimentam aggregateRating no JSON-LD das páginas de plano e
// category — coleta real, sem fake. Google Search Console penaliza
// ratings fabricados.
type Review struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	OrderID      string    `json:"order_id"`
	PlanID       string    `json:"plan_id"`
	PlanCategory string    `json:"plan_category"`
	CountryCode  string    `json:"country_code"`
	Rating       int       `json:"rating"` // 1..5
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	Visible      bool      `json:"visible"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// PublicReview é a forma exposta no front (sem user_id / order_id pra
// não vazar PII em rich results). Hidrata o nome do cliente (primeiro nome
// + inicial do sobrenome — "John D.") pra dar humanização sem comprometer.
type PublicReview struct {
	Rating     int       `json:"rating"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	AuthorName string    `json:"author_name"`
	CreatedAt  time.Time `json:"created_at"`
}

// AggregateRating é o resumo estatístico de reviews — usado pelo JSON-LD
// schema.org/AggregateRating. Conta só reviews com visible=true.
type AggregateRating struct {
	RatingValue float64 `json:"rating_value"` // média de 1..5, 2 casas
	ReviewCount int     `json:"review_count"`
	BestRating  int     `json:"best_rating"` // sempre 5
	WorstRating int     `json:"worst_rating"`// sempre 1
}

type ReviewRepository interface {
	Create(ctx context.Context, r Review) error
	GetByOrderID(ctx context.Context, orderID string) (*Review, error)
	// ListPublicByPlan retorna os últimos N reviews visíveis pra um plano,
	// pronto pra renderizar como social proof no front.
	ListPublicByPlan(ctx context.Context, planID string, limit int) ([]PublicReview, error)
	ListPublicByCategory(ctx context.Context, category string, limit int) ([]PublicReview, error)
	// AggregateByPlan computa AggregateRating do plano. Devolve nil quando
	// não há reviews visíveis (callers omitem o bloco no JSON-LD nesse caso).
	AggregateByPlan(ctx context.Context, planID string) (*AggregateRating, error)
	AggregateByCategory(ctx context.Context, category string) (*AggregateRating, error)
	// SetVisibility é a moderação manual (admin).
	SetVisibility(ctx context.Context, id string, visible bool) error
	// ListAdmin devolve os últimos N reviews pra moderação. Inclui invisíveis,
	// hidrata user.name/email e plan.name pra UI mostrar contexto sem N+1.
	ListAdmin(ctx context.Context, filter AdminReviewFilter, limit int) ([]AdminReview, error)
	GetByID(ctx context.Context, id string) (*Review, error)
}

// AdminReviewFilter — filtros opcionais pro ListAdmin.
type AdminReviewFilter struct {
	OnlyHidden bool   // true → só não-visíveis (pra fila de moderação)
	PlanID     string // opcional
	Category   string // opcional
}

// AdminReview = Review + hidratação básica de user/plan pra UI.
type AdminReview struct {
	Review
	UserName  string `json:"user_name"`
	UserEmail string `json:"user_email"`
	PlanName  string `json:"plan_name"`
}

// ReviewRequestCandidate é um pedido elegível pra receber o email de pedido
// de review: paid há > N dias, sem review ainda, sem email já enviado.
type ReviewRequestCandidate struct {
	OrderID   string
	UserID    string
	UserName  string
	UserEmail string
	PlanName  string
}

// ReviewRequestRepository agrupa as queries que o cron de envio precisa.
// Separado do ReviewRepository pra deixar claro o boundary de leitura.
type ReviewRequestRepository interface {
	// ListReadyForReviewRequest devolve orders paid há >= olderThan, sem
	// review, sem review_email_sent_at. Hidrata o user.email/name e plan.name
	// pra o cron preencher o template sem fazer N lookups.
	ListReadyForReviewRequest(ctx context.Context, olderThan time.Time, limit int) ([]ReviewRequestCandidate, error)
	// MarkReviewEmailSent atualiza orders.review_email_sent_at=NOW().
	MarkReviewEmailSent(ctx context.Context, orderID string) error
}
