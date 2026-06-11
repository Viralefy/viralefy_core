// orders-anonymize-cron — anonimização IRREVERSÍVEL de PII em orders
// expirados pela retenção fiscal de 5 anos (Receita Federal Art. 195 CTN
// + Resolução RFB 2.169/2023).
//
// PROBLEMA QUE RESOLVE
// --------------------
// LGPD Art. 15 II + Art. 16 obriga que dados pessoais sejam descartados
// quando expira a finalidade legal de retenção. A finalidade que justifica
// guarda do snapshot PII em `orders` (`email_at_purchase`, `name_at_purchase`)
// é estritamente FISCAL — Receita exige 5 anos. Após esse prazo o
// snapshot deixa de ter base legal Art. 7 II e vira coleta excessiva.
//
// COMPORTAMENTO
// -------------
//   - Monthly cron: ações em massa são raras (rows expirando em qualquer
//     mês são poucos — ~30 dias de "elegibilidade nova" por execução).
//     Daily é desperdício de I/O em DB grande.
//   - Critério SQL: status='paid' AND updated_at < NOW() - INTERVAL '5 years'
//     AND (email_at_purchase IS NOT NULL OR name_at_purchase IS NOT NULL).
//     Usamos `updated_at` (porque a coluna `paid_at` vive em `invoices`,
//     não em `orders` — verificado no schema 007_profiles_credits) +
//     `status='paid'` para garantir que pegamos só orders que de fato
//     virou paid (não pending abandonados). Em `orders`, `updated_at` é
//     atualizado por `MarkOrderPaid` (order_repo.go:152) no momento da
//     mudança pra status='paid', então é uma proxy de "paid_at" estável
//     (orders pagos não voltam ao limbo).
//
//   - Reforço: orders abandonados sem status='paid' são limpos por
//     outros TTLs (`status='pending' AND created_at < ...` em outro
//     ciclo); aqui só tocamos no que tem retenção fiscal real.
//   - UPDATE: email_at_purchase='[ANONYMIZED]', name_at_purchase='[ANONYMIZED]'.
//     **NÃO** usamos NULL — manter sentinela permite distinguir
//     "anonimizado por retenção" de "nunca teve snapshot" (orders pre-
//     migration 041 ainda podem ter colunas NULL).
//   - Preserva integralmente: id, total_cents, currency, gateway_id,
//     external_ref, paid_at, status, plan_id, qty_units, fee_cents,
//     net_cents — TODOS os campos fiscais.
//   - Idempotente: o WHERE filtra rows já anonimizadas
//     (email_at_purchase != '[ANONYMIZED]'); rerun é no-op.
//   - Atômico por batch de 1000 (BEGIN/COMMIT por chunk) pra não segurar
//     lock longo em tabela quente. Em 5y a contagem deve ser baixa, mas
//     defensivo importa quando volume aparecer.
//
// MÉTRICAS (textfile collector)
// -----------------------------
//   - viralefy_orders_anonymized_total          counter (rows tocadas no run)
//   - viralefy_orders_anonymize_pending_count   gauge (rows ainda elegíveis após o run — drift)
//   - viralefy_orders_anonymize_last_run_timestamp_seconds gauge
//
// O QUE NÃO FAZEMOS (escopo proposital)
// -------------------------------------
//   - NÃO deletamos rows. Orders >5y permanecem na tabela com PII
//     redacted — métricas agregadas + reconcile histórico continuam OK.
//   - NÃO tocamos invoices, order_refunds, audit_log — escopo é só
//     orders. Esses têm cronograma fiscal próprio.
//   - NÃO mexe em user_id (Fkey já é NULL pra orders de users excluídos
//     via deletion-cron; pra orders de users ativos preservamos a Fkey
//     porque o user pode acessar histórico).
//
// Build:
//
//	cd viralefy_core && go build -o bin/orders-anonymize-cron ./cmd/orders-anonymize-cron
//
// Uso:
//
//	DATABASE_URL=postgres://... ./orders-anonymize-cron
//	DATABASE_URL=postgres://... DRY_RUN=1 ./orders-anonymize-cron
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// report é o envelope serializado em stdout (Loki-friendly).
type report struct {
	Timestamp        string `json:"timestamp"`
	DurationMs       int64  `json:"duration_ms"`
	DryRun           bool   `json:"dry_run"`
	Anonymized       int64  `json:"anonymized"`        // rows tocadas neste run
	PendingRemaining int64  `json:"pending_remaining"` // rows ainda elegíveis após o run
	Batches          int    `json:"batches"`           // quantos COMMITs
	Error            string `json:"error,omitempty"`
}

// defaultTextfilePath segue mesma convenção dos demais crons —
// node_exporter expõe arquivos *.prom de lá automaticamente.
const defaultTextfilePath = "/var/lib/node_exporter/textfile_collector/viralefy_orders_anonymize.prom"

// batchSize controla o tamanho da transação. 1000 é um balanço entre
// I/O eficiente e lock holding curto. Em 5y o universo elegível é
// pequeno, mas defensivo importa.
const batchSize = 1000

// sentinel é o placeholder que substitui o PII. Distingue "anonimizado
// por retenção" de "nunca teve snapshot" (NULL) — útil pra debug
// forense + idempotência (o WHERE exclui sentinel).
const sentinel = "[ANONYMIZED]"

func main() {
	start := time.Now()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "FATAL: DATABASE_URL não setada")
		os.Exit(2)
	}
	dryRun := os.Getenv("DRY_RUN") == "1"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: conexão DB: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: ping DB: %v\n", err)
		os.Exit(2)
	}

	rep := report{
		Timestamp: start.UTC().Format(time.RFC3339),
		DryRun:    dryRun,
	}

	// Conta antes — pra mostrar quantos rows estavam pendentes no início.
	preCount, err := countEligible(ctx, pool)
	if err != nil {
		rep.Error = fmt.Sprintf("count eligible (pre): %v", err)
		emit(rep, time.Since(start))
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "[info] pre-run pending=%d dry_run=%v\n", preCount, dryRun)

	if !dryRun {
		// Loop de batches: anonimiza até esgotar elegíveis ou bater limite
		// de segurança (10 batches × 1000 = 10k rows / run — improvável
		// estourar mensalmente, mas o loop limita blast radius).
		const maxBatches = 10
		for i := 0; i < maxBatches; i++ {
			n, err := anonymizeBatch(ctx, pool)
			if err != nil {
				rep.Error = fmt.Sprintf("batch %d: %v", i+1, err)
				break
			}
			if n == 0 {
				break // esgotou
			}
			rep.Anonymized += n
			rep.Batches++
			fmt.Fprintf(os.Stderr, "[info] batch %d anonymized=%d\n", i+1, n)
		}
	} else {
		rep.Anonymized = preCount // dry-run: simula que tudo seria tocado
	}

	// Conta depois — drift residual (idealmente 0).
	postCount, perr := countEligible(ctx, pool)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "[warn] count eligible (post): %v\n", perr)
	}
	rep.PendingRemaining = postCount

	emit(rep, time.Since(start))

	// Métricas pro Prometheus
	writeTextfileMetrics(rep)

	// Exit 1 quando o run falhou no meio (rep.Error setado) — systemd
	// marca unit failed e ops vê no journal.
	if rep.Error != "" {
		os.Exit(1)
	}
}

// countEligible conta rows que SERIAM anonimizadas. Idempotente — o
// WHERE também exclui já-anonimizadas pra rerun ficar correto.
//
// Critério: status='paid' AND updated_at < NOW() - 5y. Vide doc do
// pacote pra raciocínio (paid_at não existe em orders; updated_at é
// proxy estável pós-MarkOrderPaid).
func countEligible(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM orders
		 WHERE status = 'paid'
		   AND updated_at < NOW() - INTERVAL '5 years'
		   AND (
		         (email_at_purchase IS NOT NULL AND email_at_purchase <> $1)
		      OR (name_at_purchase  IS NOT NULL AND name_at_purchase  <> $1)
		       )`, sentinel).Scan(&n)
	return n, err
}

// anonymizeBatch anonimiza até batchSize rows numa única transação.
// Retorna quantas rows foram tocadas (0 = esgotou). Usa CTE com
// SELECT ... FOR UPDATE SKIP LOCKED pra não travar com outro run
// concorrente (improvável mas defensivo).
func anonymizeBatch(parent context.Context, pool *pgxpool.Pool) (int64, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	tag, err := pool.Exec(ctx, `
		WITH due AS (
			SELECT id
			  FROM orders
			 WHERE status = 'paid'
			   AND updated_at < NOW() - INTERVAL '5 years'
			   AND (
			         (email_at_purchase IS NOT NULL AND email_at_purchase <> $1)
			      OR (name_at_purchase  IS NOT NULL AND name_at_purchase  <> $1)
			       )
			 ORDER BY updated_at ASC
			 LIMIT $2
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE orders o
		   SET email_at_purchase = $1,
		       name_at_purchase  = $1
		  FROM due
		 WHERE o.id = due.id`, sentinel, batchSize)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// emit serializa o report no stdout. Estruturado pra Loki indexar.
func emit(rep report, elapsed time.Duration) {
	rep.DurationMs = elapsed.Milliseconds()
	_ = json.NewEncoder(os.Stdout).Encode(rep)
}

// writeTextfileMetrics emite o .prom no path configurado. Atômico via
// write-then-rename. Mesmo padrão de user-deletion-cron.
func writeTextfileMetrics(rep report) {
	path := os.Getenv("TEXTFILE_PATH")
	if path == "" {
		path = defaultTextfilePath
	}
	if path == "-" { // opt-out explícito (testes)
		return
	}

	var sb strings.Builder
	sb.WriteString("# HELP viralefy_orders_anonymized_total Total de orders anonimizados na última passagem do cron (PII redacted após retenção fiscal 5y).\n")
	sb.WriteString("# TYPE viralefy_orders_anonymized_total counter\n")
	fmt.Fprintf(&sb, "viralefy_orders_anonymized_total %d\n", rep.Anonymized)

	sb.WriteString("# HELP viralefy_orders_anonymize_pending_count Orders ainda elegíveis pra anonimização após o run (drift — esperado 0).\n")
	sb.WriteString("# TYPE viralefy_orders_anonymize_pending_count gauge\n")
	fmt.Fprintf(&sb, "viralefy_orders_anonymize_pending_count %d\n", rep.PendingRemaining)

	sb.WriteString("# HELP viralefy_orders_anonymize_last_run_timestamp_seconds Unix timestamp da última execução do cron.\n")
	sb.WriteString("# TYPE viralefy_orders_anonymize_last_run_timestamp_seconds gauge\n")
	fmt.Fprintf(&sb, "viralefy_orders_anonymize_last_run_timestamp_seconds %d\n", time.Now().Unix())

	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] textfile mkdir: %v\n", err)
		return
	}
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] textfile write: %v\n", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] textfile rename: %v\n", err)
	}
}
