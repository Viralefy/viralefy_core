// Tests para classifyStripeIdempotencyResult — espelha o fix HIGH do
// round 25 (StripeWebhook idempotency).
//
// O ponto inteiro do helper é dar table-driven coverage SEM precisar de
// mocking pesado do pgxpool. Os 4 casos do enunciado:
//
//  - event_id novo (INSERT OK, rows=1) → Proceed
//  - event_id já existe (ON CONFLICT, rows=0, err nil) → Duplicate
//  - unique_violation (err pgError 23505) → Duplicate
//  - erro transitório (timeout, conn closed, etc) → TransientError
package http

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyStripeIdempotencyResult_TableDriven(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		rows int64
		err  error
		want stripeIdempotencyDecision
	}{
		{
			name: "insert_ok_new_event",
			rows: 1,
			err:  nil,
			want: idempotencyProceed,
		},
		{
			name: "on_conflict_do_nothing_duplicate",
			// ON CONFLICT DO NOTHING devolve rows=0 sem erro quando a unique
			// constraint pega o INSERT. É o caminho FELIZ de "Stripe re-entregou".
			rows: 0,
			err:  nil,
			want: idempotencyDuplicate,
		},
		{
			name: "unique_violation_pg_error_23505",
			// Em alguns shapes (constraints diferidas, triggers) o pg pode
			// emergir 23505 mesmo com ON CONFLICT no statement. Trate como
			// duplicate igual o ON CONFLICT — não chama MarkOrderPaid.
			rows: 0,
			err:  &pgconn.PgError{Code: "23505", Message: "duplicate key value"},
			want: idempotencyDuplicate,
		},
		{
			name: "unique_violation_wrapped",
			// fmt.Errorf("wrap: %w", pgErr) — errors.As tem que ainda achar.
			rows: 0,
			err:  fmt.Errorf("repo wrap: %w", &pgconn.PgError{Code: "23505"}),
			want: idempotencyDuplicate,
		},
		{
			name: "transient_context_deadline",
			// Timeout do pgx vira context.DeadlineExceeded ou um pgError de
			// connection. Qualquer um NÃO pode virar Proceed — caso contrário,
			// a próxima entrega do Stripe (que vai tentar de novo) faz dupla
			// chamada de MarkOrderPaid. Tem que ser 500.
			rows: 0,
			err:  context.DeadlineExceeded,
			want: idempotencyTransientError,
		},
		{
			name: "transient_generic_db_error",
			rows: 0,
			err:  errors.New("connection reset by peer"),
			want: idempotencyTransientError,
		},
		{
			name: "transient_pg_serialization_failure_not_23505",
			rows: 0,
			err:  &pgconn.PgError{Code: "40001", Message: "serialization_failure"},
			want: idempotencyTransientError,
		},
		{
			name: "transient_pg_fk_violation_23503_not_duplicate",
			// 23503 é foreign_key_violation — NÃO é unique. Deve cair em
			// transient, não em duplicate (senão swallow um bug de wiring).
			rows: 0,
			err:  &pgconn.PgError{Code: "23503", Message: "fk violation"},
			want: idempotencyTransientError,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyStripeIdempotencyResult(tc.rows, tc.err)
			if got != tc.want {
				t.Errorf("classifyStripeIdempotencyResult(rows=%d, err=%v) = %d, want %d",
					tc.rows, tc.err, got, tc.want)
			}
		})
	}
}

// Self-check anti-"verde mentiroso" (padrões §22.8): se mexer no helper e
// fazer ele responder Proceed pra erro transitório, este teste TEM que
// falhar — é o ponto inteiro do fix.
func TestClassifyStripeIdempotencyResult_TransientNeverProceeds(t *testing.T) {
	got := classifyStripeIdempotencyResult(0, errors.New("anything"))
	if got == idempotencyProceed {
		t.Fatal("ERRO DE SEGURANÇA: erro transitório NUNCA pode virar Proceed " +
			"— senão Stripe re-entrega e MarkOrderPaid dispara em dupla")
	}
}
