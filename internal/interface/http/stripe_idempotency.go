// Round 25 HIGH fix: a classificação do INSERT em stripe_events_processed
// vivia inline no StripeWebhook. ANTES, qualquer erro do DB no INSERT caía
// num `logger.Warn(...)` que NÃO retornava — o handler seguia direto pro
// MarkOrderPaid, abrindo brecha de double-fire se o INSERT falhasse por
// motivo transitório (timeout, conn closed) e Stripe re-entregasse.
//
// Isolamos a decisão aqui pra testar table-driven SEM mockar `pgxpool`:
// o helper recebe `rowsAffected` + `err` (igual ao que o handler tem em
// mãos depois do Exec) e devolve uma das três decisões:
//
//   - idempotencyProceed         → INSERT registrou (rows=1) — pode chamar MarkOrderPaid
//   - idempotencyDuplicate       → event_id JÁ processado (ON CONFLICT rows=0
//                                  OU unique_violation 23505) — ACK 200, NÃO chama
//                                  MarkOrderPaid
//   - idempotencyTransientError  → qualquer outro erro de DB — 500, Stripe
//                                  re-entrega; tentativa futura pega o INSERT limpo
package http

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

type stripeIdempotencyDecision int

const (
	idempotencyProceed stripeIdempotencyDecision = iota
	idempotencyDuplicate
	idempotencyTransientError
)

// classifyStripeIdempotencyResult traduz o par (rowsAffected, err) que vem
// do Exec(`INSERT ... ON CONFLICT DO NOTHING`) em uma decisão segura.
//
// Regras (na ordem):
//
//  1. Se err != nil e é unique_violation (PG SQLSTATE 23505) → Duplicate.
//     Pode acontecer se a unique constraint dispara antes do ON CONFLICT
//     resolver (não é o caminho típico, mas o pgx pode emergir o pgError
//     em certas concorrências/triggers).
//  2. Se err != nil de qualquer outro tipo → TransientError. Sem isso,
//     o caller seguiria pro MarkOrderPaid sem garantia de idempotência.
//  3. Se err == nil e rowsAffected == 0 → Duplicate (caminho normal do
//     ON CONFLICT DO NOTHING: registro já existia).
//  4. Se err == nil e rowsAffected > 0 → Proceed (INSERT novo, vamos
//     processar).
func classifyStripeIdempotencyResult(rowsAffected int64, err error) stripeIdempotencyDecision {
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return idempotencyDuplicate
		}
		return idempotencyTransientError
	}
	if rowsAffected == 0 {
		return idempotencyDuplicate
	}
	return idempotencyProceed
}
