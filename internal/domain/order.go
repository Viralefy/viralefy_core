package domain

import (
	"context"
	"time"
)

type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"
	OrderStatusPaid      OrderStatus = "paid"
	OrderStatusFailed    OrderStatus = "failed"
	OrderStatusCancelled OrderStatus = "cancelled"
)

type Order struct {
	ID                 string            `json:"id"`
	UserID             string            `json:"user_id"`
	PlanID             string            `json:"plan_id"`
	Status             OrderStatus       `json:"status"`
	AmountCents        int               `json:"amount_cents"`
	Currency           string            `json:"currency"`
	// Tax (Fase 5.3) — VAT EU/GB. amount_cents JÁ inclui tax_usd_cents.
	TaxCountryCode     string            `json:"tax_country_code,omitempty"`
	TaxRatePct         float64           `json:"tax_rate_pct,omitempty"`
	TaxUSDCents        int               `json:"tax_usd_cents,omitempty"`
	// TargetCountryCode é o mercado da entrega — ex.: "de" se comprou na
	// LP /de/instagram-followers (operador sabe que tem que pegar supplier
	// alemão). Independente do tax_country (país do comprador).
	TargetCountryCode  string            `json:"target_country_code,omitempty"`
	DisplayCurrency    string            `json:"display_currency"`
	DisplayAmount      string            `json:"display_amount"`
	SettlementCurrency string            `json:"settlement_currency"`
	SettlementAmount   string            `json:"settlement_amount"`
	GatewayID          *string           `json:"gateway_id,omitempty"`
	ExternalRef        *string           `json:"external_ref,omitempty"`
	PaymentURL         *string           `json:"payment_url,omitempty"`
	PaymentExtra       map[string]string `json:"payment_extra,omitempty"`
	ProfileID          *string           `json:"profile_id,omitempty"`
	PublicationURL     *string           `json:"publication_url,omitempty"`
	PaymentMethod      string            `json:"payment_method"`     // gateway | credits
	CreditsUsedCents   int               `json:"credits_used_cents"` // se payment_method=credits
	// CustomData carrega snapshot do formulário customizado da categoria
	// (ex.: account recovery — data do banimento, motivo estimado, última
	// publicação). Schema livre por categoria; backend não interpreta, só
	// repassa pro ticket aberto após pagamento.
	CustomData         map[string]any    `json:"custom_data,omitempty"`
	// Tracking carrega UTM/fbclid/gclid/referrer/landing_url/ip/user_agent.
	// Usado pra anti-fraude (mesmo client_id + perfis comprando em loop)
	// e CAPI/Events API (Meta/Google/TikTok) com event_source_url + click_id.
	Tracking           map[string]any    `json:"tracking,omitempty"`
	// Baseline + delivery metrics. Snapshots públicos do alvo antes do
	// gateway começar (baseline) e depois (delivery). Discrepância entre
	// (delivery - baseline) e order.followers_qty sinaliza falha de
	// entrega independente do que o gateway respondeu.
	BaselineMetrics    map[string]any    `json:"baseline_metrics,omitempty"`
	BaselineCapturedAt *time.Time        `json:"baseline_captured_at,omitempty"`
	BaselineSource     *string           `json:"baseline_source,omitempty"`
	DeliveryMetrics    map[string]any    `json:"delivery_metrics,omitempty"`
	DeliveryCapturedAt *time.Time        `json:"delivery_captured_at,omitempty"`
	DeliverySource     *string           `json:"delivery_source,omitempty"`
	// TicketID linka o pedido ao ticket aberto automaticamente quando
	// `Status` virou `paid` em categorias que abrem ticket (recovery,
	// BMs, perfis).
	TicketID           *string           `json:"ticket_id,omitempty"`
	// Proof of payment (migration 034). Cliente anexa comprovante após
	// depositar manualmente (PIX, crypto on-chain). Admin revisa em
	// backoffice e marca pago. ProofStatus: pending | approved | rejected.
	ProofURL           *string           `json:"proof_url,omitempty"`
	ProofUploadedAt    *time.Time        `json:"proof_uploaded_at,omitempty"`
	ProofStatus        *string           `json:"proof_status,omitempty"`
	ProofNote          *string           `json:"proof_note,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// OrderView é um read-model de pedido enriquecido com dados do plano,
// usado em histórico de compras e listagem admin.
type OrderView struct {
	Order
	PlanName     string `json:"plan_name"`
	PlanCategory string `json:"plan_category"`
	// Hidratados a partir de users via JOIN. Listagem admin mostra
	// nome do cliente em vez de UUID; histórico do usuário não usa
	// (já está logado, sabe quem ele é).
	UserName  string `json:"user_name,omitempty"`
	UserEmail string `json:"user_email,omitempty"`
}

type OrderRepository interface {
	Create(ctx context.Context, o Order) error
	GetByID(ctx context.Context, id string) (*Order, error)
	GetByExternalRef(ctx context.Context, externalRef string) (*Order, error)
	ListByUser(ctx context.Context, userID string) ([]Order, error)
	ListViewByUser(ctx context.Context, userID string) ([]OrderView, error)
	ListAll(ctx context.Context) ([]Order, error)
	ListAllView(ctx context.Context) ([]OrderView, error)
	UpdateStatus(ctx context.Context, id string, status OrderStatus, externalRef *string) error
	UpdatePayment(ctx context.Context, id, externalRef, paymentURL string, extra map[string]string) error
	// LinkTicket associa um ticket aberto pós-pagamento ao pedido.
	LinkTicket(ctx context.Context, orderID, ticketID string) error
	// SetBaselineMetrics grava snapshot do alvo PRÉ-entrega (followers/
	// likes/views públicas via scrape). source identifica o método.
	SetBaselineMetrics(ctx context.Context, orderID string, metrics map[string]any, source string) error
	// SetDeliveryMetrics grava o snapshot pós-entrega usado pra verificar
	// se o gateway efetivamente entregou.
	SetDeliveryMetrics(ctx context.Context, orderID string, metrics map[string]any, source string) error
	// ListReadyForDeliveryCapture devolve pedidos em status=paid que ainda
	// não tiveram delivery capturado e cuja última atualização foi anterior
	// a `olderThan`. Usado pelo cron de delivery capture (24h pós-pago) pra
	// rodar o scrape de fonte secundária sem depender do admin. Limita a `n`
	// pra capar a rajada por iteração; cron roda em intervalo e pega o resto
	// nos próximos ticks.
	ListReadyForDeliveryCapture(ctx context.Context, olderThan time.Time, limit int) ([]Order, error)
	// AssignGateway grava gateway_id num pedido pending. Usado pelo fluxo
	// novo de checkout (pick-method) onde o gateway é escolhido APÓS a
	// criação da order. Falha se o pedido já está paid/cancelled.
	AssignGateway(ctx context.Context, orderID, gatewayID string) error
	// SetProof persiste o comprovante anexado pelo cliente. status default
	// "pending" (admin precisa revisar). url é opaco — pode ser data URL
	// curto ou http url de storage; tamanho/validação no service.
	SetProof(ctx context.Context, orderID, fileURL, fileName, mime, note string, sizeBytes int) error
	// SetProofStatus atualiza proof_status (approved | rejected) — usado
	// pelo backoffice quando admin revisa o comprovante. Não dispara
	// mark-as-paid: aprovação semântica só, side-effect é responsabilidade
	// do PaymentReceiver chamado em sequência pelo handler.
	SetProofStatus(ctx context.Context, orderID, status, reviewerNote string) error
	// ListPendingProofs devolve pedidos com proof_status='pending' pra
	// fila de revisão no backoffice. Ordena por proof_uploaded_at ASC
	// (mais antigos primeiro — SLA tracker).
	ListPendingProofs(ctx context.Context, limit int) ([]OrderView, error)
}
