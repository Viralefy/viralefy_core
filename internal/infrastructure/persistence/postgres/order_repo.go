package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type OrderRepo struct{ db *DB }

func NewOrderRepo(db *DB) *OrderRepo { return &OrderRepo{db: db} }

const orderCols = `id, user_id, plan_id, status, amount_cents, currency,
	display_currency, display_amount, settlement_currency, settlement_amount,
	gateway_id, external_ref, payment_url, payment_extra,
	profile_id, publication_url, payment_method, credits_used_cents,
	custom_data, ticket_id, tracking,
	baseline_metrics, baseline_captured_at, baseline_source,
	delivery_metrics, delivery_captured_at, delivery_source,
	COALESCE(tax_country_code,''), COALESCE(tax_rate_pct,0), COALESCE(tax_usd_cents,0),
	COALESCE(target_country_code,''),
	proof_url, proof_uploaded_at, proof_status, proof_note,
	created_at, updated_at`

func (r *OrderRepo) Create(ctx context.Context, o domain.Order) error {
	extra, _ := json.Marshal(o.PaymentExtra)
	if len(extra) == 0 {
		extra = []byte("{}")
	}
	custom, _ := json.Marshal(o.CustomData)
	if len(custom) == 0 {
		custom = []byte("{}")
	}
	tracking, _ := json.Marshal(o.Tracking)
	if len(tracking) == 0 {
		tracking = []byte("{}")
	}
	if o.PaymentMethod == "" {
		o.PaymentMethod = "gateway"
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO orders (id, user_id, plan_id, status, amount_cents, currency,
			display_currency, display_amount, settlement_currency, settlement_amount,
			gateway_id, payment_extra,
			profile_id, publication_url, payment_method, credits_used_cents,
			custom_data, ticket_id, tracking,
			tax_country_code, tax_rate_pct, tax_usd_cents,
			target_country_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,
			NULLIF($20,''),NULLIF($21,0)::numeric,$22,
			NULLIF($23,''))`,
		o.ID, o.UserID, o.PlanID, o.Status, o.AmountCents, o.Currency,
		o.DisplayCurrency, o.DisplayAmount, o.SettlementCurrency, o.SettlementAmount,
		o.GatewayID, extra,
		o.ProfileID, o.PublicationURL, o.PaymentMethod, o.CreditsUsedCents,
		custom, o.TicketID, tracking,
		o.TaxCountryCode, o.TaxRatePct, o.TaxUSDCents,
		o.TargetCountryCode)
	return err
}

// LinkTicket marca o ticket que foi aberto pro pedido (após pagamento
// confirmado em categorias com handoff manual).
func (r *OrderRepo) LinkTicket(ctx context.Context, orderID, ticketID string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE orders SET ticket_id=$2, updated_at=NOW() WHERE id=$1`, orderID, ticketID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *OrderRepo) GetByID(ctx context.Context, id string) (*domain.Order, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE id=$1`, id)
	return scanOrder(row)
}

func (r *OrderRepo) GetByExternalRef(ctx context.Context, ref string) (*domain.Order, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE external_ref=$1 LIMIT 1`, ref)
	return scanOrder(row)
}

func (r *OrderRepo) ListByUser(ctx context.Context, userID string) ([]domain.Order, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+orderCols+`
		FROM orders WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrders(rows)
}

func (r *OrderRepo) ListAll(ctx context.Context) ([]domain.Order, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+orderCols+`
		FROM orders ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrders(rows)
}

const orderViewCols = `o.id, o.user_id, o.plan_id, o.status, o.amount_cents, o.currency,
	o.display_currency, o.display_amount, o.settlement_currency, o.settlement_amount,
	o.gateway_id, o.external_ref, o.payment_url, o.payment_extra,
	o.profile_id, o.publication_url, o.payment_method, o.credits_used_cents,
	o.custom_data, o.ticket_id, o.tracking,
	o.baseline_metrics, o.baseline_captured_at, o.baseline_source,
	o.delivery_metrics, o.delivery_captured_at, o.delivery_source,
	o.proof_url, o.proof_uploaded_at, o.proof_status, o.proof_note,
	o.created_at, o.updated_at,
	COALESCE(p.name, ''), COALESCE(p.category, ''),
	COALESCE(u.name, ''), COALESCE(u.email, '')`

const orderViewFrom = `FROM orders o
		LEFT JOIN plans p ON p.id = o.plan_id
		LEFT JOIN users u ON u.id = o.user_id`

func (r *OrderRepo) ListViewByUser(ctx context.Context, userID string) ([]domain.OrderView, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+orderViewCols+`
		`+orderViewFrom+`
		WHERE o.user_id=$1 ORDER BY o.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrderViews(rows)
}

func (r *OrderRepo) ListAllView(ctx context.Context) ([]domain.OrderView, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+orderViewCols+`
		`+orderViewFrom+`
		ORDER BY o.created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrderViews(rows)
}

func (r *OrderRepo) UpdateStatus(ctx context.Context, id string, status domain.OrderStatus, externalRef *string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE orders SET status=$2, external_ref=$3, updated_at=NOW() WHERE id=$1`, id, status, externalRef)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *OrderRepo) UpdatePayment(ctx context.Context, id, externalRef, paymentURL string, extra map[string]string) error {
	raw, _ := json.Marshal(extra)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE orders SET external_ref=$2, payment_url=$3, payment_extra=$4, updated_at=NOW()
		WHERE id=$1`, id, nullable(externalRef), nullable(paymentURL), raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func scanOrders(rows pgx.Rows) ([]domain.Order, error) {
	list := []domain.Order{}
	for rows.Next() {
		o, err := scanOrderRow(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *o)
	}
	return list, rows.Err()
}

func scanOrder(row pgx.Row) (*domain.Order, error) {
	o, err := scanOrderRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return o, err
}

func scanOrderRow(row pgx.Row) (*domain.Order, error) {
	var o domain.Order
	var extra, custom, tracking, baseline, delivery []byte
	err := row.Scan(&o.ID, &o.UserID, &o.PlanID, &o.Status, &o.AmountCents, &o.Currency,
		&o.DisplayCurrency, &o.DisplayAmount, &o.SettlementCurrency, &o.SettlementAmount,
		&o.GatewayID, &o.ExternalRef, &o.PaymentURL, &extra,
		&o.ProfileID, &o.PublicationURL, &o.PaymentMethod, &o.CreditsUsedCents,
		&custom, &o.TicketID, &tracking,
		&baseline, &o.BaselineCapturedAt, &o.BaselineSource,
		&delivery, &o.DeliveryCapturedAt, &o.DeliverySource,
		&o.TaxCountryCode, &o.TaxRatePct, &o.TaxUSDCents,
		&o.TargetCountryCode,
		&o.ProofURL, &o.ProofUploadedAt, &o.ProofStatus, &o.ProofNote,
		&o.CreatedAt, &o.UpdatedAt)
	if err == nil {
		o.PaymentExtra = map[string]string{}
		if len(extra) > 0 {
			_ = json.Unmarshal(extra, &o.PaymentExtra)
		}
		o.CustomData = map[string]any{}
		if len(custom) > 0 {
			_ = json.Unmarshal(custom, &o.CustomData)
		}
		o.Tracking = map[string]any{}
		if len(tracking) > 0 {
			_ = json.Unmarshal(tracking, &o.Tracking)
		}
		if len(baseline) > 0 {
			o.BaselineMetrics = map[string]any{}
			_ = json.Unmarshal(baseline, &o.BaselineMetrics)
		}
		if len(delivery) > 0 {
			o.DeliveryMetrics = map[string]any{}
			_ = json.Unmarshal(delivery, &o.DeliveryMetrics)
		}
	}
	return &o, err
}

func scanOrderViews(rows pgx.Rows) ([]domain.OrderView, error) {
	list := []domain.OrderView{}
	for rows.Next() {
		var v domain.OrderView
		var extra, custom, tracking, baseline, delivery []byte
		err := rows.Scan(&v.ID, &v.UserID, &v.PlanID, &v.Status, &v.AmountCents, &v.Currency,
			&v.DisplayCurrency, &v.DisplayAmount, &v.SettlementCurrency, &v.SettlementAmount,
			&v.GatewayID, &v.ExternalRef, &v.PaymentURL, &extra,
			&v.ProfileID, &v.PublicationURL, &v.PaymentMethod, &v.CreditsUsedCents,
			&custom, &v.TicketID, &tracking,
			&baseline, &v.BaselineCapturedAt, &v.BaselineSource,
			&delivery, &v.DeliveryCapturedAt, &v.DeliverySource,
			&v.ProofURL, &v.ProofUploadedAt, &v.ProofStatus, &v.ProofNote,
			&v.CreatedAt, &v.UpdatedAt,
			&v.PlanName, &v.PlanCategory,
			&v.UserName, &v.UserEmail)
		if err != nil {
			return nil, err
		}
		v.PaymentExtra = map[string]string{}
		if len(extra) > 0 {
			_ = json.Unmarshal(extra, &v.PaymentExtra)
		}
		v.CustomData = map[string]any{}
		if len(custom) > 0 {
			_ = json.Unmarshal(custom, &v.CustomData)
		}
		v.Tracking = map[string]any{}
		if len(tracking) > 0 {
			_ = json.Unmarshal(tracking, &v.Tracking)
		}
		if len(baseline) > 0 {
			v.BaselineMetrics = map[string]any{}
			_ = json.Unmarshal(baseline, &v.BaselineMetrics)
		}
		if len(delivery) > 0 {
			v.DeliveryMetrics = map[string]any{}
			_ = json.Unmarshal(delivery, &v.DeliveryMetrics)
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// SetBaselineMetrics grava o snapshot pré-entrega. Idempotente: re-runs
// sobrescrevem (caso o operador chame manualmente após uma falha de scrape).
func (r *OrderRepo) SetBaselineMetrics(ctx context.Context, orderID string, metrics map[string]any, source string) error {
	raw, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE orders SET baseline_metrics=$2::jsonb, baseline_captured_at=NOW(),
		                  baseline_source=$3, updated_at=NOW()
		 WHERE id=$1`, orderID, raw, source)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *OrderRepo) SetDeliveryMetrics(ctx context.Context, orderID string, metrics map[string]any, source string) error {
	raw, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE orders SET delivery_metrics=$2::jsonb, delivery_captured_at=NOW(),
		                  delivery_source=$3, updated_at=NOW()
		 WHERE id=$1`, orderID, raw, source)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListReadyForDeliveryCapture devolve pedidos pagos sem snapshot de delivery
// e cuja última atualização foi anterior a `olderThan` (proxy de "ficou
// pago há pelo menos N horas" — orders.paid_at não existe ainda como coluna
// dedicada; updated_at é setado quando UpdateStatus vira 'paid'). Ordena
// pelo mais antigo primeiro pra recuperar a fila de delivery atrasada
// quando o cron volta após um downtime.
func (r *OrderRepo) ListReadyForDeliveryCapture(ctx context.Context, olderThan time.Time, limit int) ([]domain.Order, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.pool.Query(ctx, `SELECT `+orderCols+`
		FROM orders
		WHERE status = 'paid'
		  AND delivery_captured_at IS NULL
		  AND updated_at < $1
		ORDER BY updated_at ASC
		LIMIT $2`, olderThan, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrders(rows)
}

// AssignGateway atribui um gateway a um pedido pending (fluxo novo de
// checkout: cliente escolhe o método de pagamento APÓS criar o pedido).
// Idempotente — pode trocar de gateway enquanto ainda pending. Bloqueia
// se o pedido já saiu de pending (paid/cancelled).
func (r *OrderRepo) AssignGateway(ctx context.Context, orderID, gatewayID string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE orders SET gateway_id=$2, updated_at=NOW()
		 WHERE id=$1 AND status = 'pending'`, orderID, gatewayID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// SetProof grava o comprovante anexado. Append em order_proofs (histórico)
// e atualiza denormalização em orders. proof_status default "pending" —
// admin marca approved/rejected via backoffice. Idempotente: re-upload
// sobrescreve denormalização e adiciona nova linha em order_proofs.
func (r *OrderRepo) SetProof(ctx context.Context, orderID, fileURL, fileName, mime, note string, sizeBytes int) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO order_proofs (id, order_id, file_url, file_name, mime_type, size_bytes, note)
		VALUES (gen_random_uuid()::text, $1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,0), NULLIF($6,''))`,
		orderID, fileURL, fileName, mime, sizeBytes, note); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders
		   SET proof_url=$2,
		       proof_uploaded_at=NOW(),
		       proof_status=COALESCE(NULLIF(proof_status,'rejected'), 'pending'),
		       proof_note=NULLIF($3,''),
		       updated_at=NOW()
		 WHERE id=$1 AND status='pending'`, orderID, fileURL, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return tx.Commit(ctx)
}

// SetProofStatus muda proof_status pra approved|rejected. NÃO valida
// o status — service deve garantir. proof_note acrescenta texto do
// reviewer; quando vazio, mantém o que já estava (não sobrescreve).
func (r *OrderRepo) SetProofStatus(ctx context.Context, orderID, status, reviewerNote string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE orders
		   SET proof_status=$2,
		       proof_note=COALESCE(NULLIF($3,''), proof_note),
		       updated_at=NOW()
		 WHERE id=$1`, orderID, status, reviewerNote)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListPendingProofs devolve a fila de comprovantes aguardando revisão.
// Ordena por upload mais antigo primeiro pra admin atacar o SLA dos
// clientes que estão esperando há mais tempo.
func (r *OrderRepo) ListPendingProofs(ctx context.Context, limit int) ([]domain.OrderView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.pool.Query(ctx, `SELECT `+orderViewCols+`
		`+orderViewFrom+`
		WHERE o.proof_status = 'pending'
		ORDER BY o.proof_uploaded_at ASC NULLS LAST
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrderViews(rows)
}
