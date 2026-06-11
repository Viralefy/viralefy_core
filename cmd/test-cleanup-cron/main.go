// test-cleanup-cron — limpa artefatos deixados pelo viralefy-smoke E2E.
//
// O smoke (post-2026-06-11) faz POST /v1/checkout REAL com tracking pra
// detectar regressão Coraza FP (incidente 2026-06-10). Isso cria:
//   - 1 user com email LIKE '%@viralefy.test'
//   - 1 order status='pending' (gateway ainda não confirmou — não vai)
//   - 1 profile órfã (new_profile.platform=instagram handle=smoketest)
//   - 0..N refresh_tokens / user_events (depende da rota; geralmente 0)
//
// Esse cron roda hourly e limpa fixtures com >2h de idade. Filtros duros:
//   - SÓ users com email LIKE '%@viralefy.test' (sufixo reservado pra test).
//   - SÓ rows com created_at < NOW() - INTERVAL '2 hours' (buffer pra
//     debug manual logo após smoke).
//   - NUNCA toca orders com status IN ('paid','confirmed') — mesmo de test
//     user. Caso (improvável) algum gateway confirme um pagamento de smoke,
//     preserva a row pra reconcile flagar.
//
// Defense-in-depth (em paralelo ao reconcile-cron, que é estritamente
// read-only por design):
//
//   - Cleanup roda em TX atômica. Falha em qualquer step → rollback total.
//   - WHERE clauses usam pattern strict `%@viralefy.test` — sem wildcard
//     livre, sem regex. Pattern reservado, jamais usado por usuário real.
//   - Logging JSON estruturado (Loki-friendly).
//   - Métricas textfile collector pra alertar se contagem explodir.
//   - DRY_RUN=1 só conta o que apagaria, não muta.
//   - Idempotente: rerun consecutivo apaga 0 rows.
//
// Build:
//
//	cd viralefy_core && go build -o bin/test-cleanup-cron ./cmd/test-cleanup-cron
//
// Uso:
//
//	DATABASE_URL=postgres://... ./test-cleanup-cron
//	DATABASE_URL=postgres://... DRY_RUN=1 ./test-cleanup-cron
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testEmailPattern é o sufixo reservado. Qualquer email com esse sufixo é
// fixture de smoke/CI; emails reais NUNCA terminam em `.test` (TLD reservado
// RFC 2606). Não parametrizar — constante on purpose, defense-in-depth.
const testEmailPattern = "%@viralefy.test"

// cleanupAgeWindow é o buffer mínimo desde a criação. Permite debug humano
// logo após smoke falhar (não apagar evidência forense em < 2h).
const cleanupAgeWindow = "2 hours"

const defaultTextfilePath = "/var/lib/node_exporter/textfile_collector/viralefy_test_cleanup.prom"

type report struct {
	Timestamp  string           `json:"timestamp"`
	DurationMs int64            `json:"duration_ms"`
	DryRun     bool             `json:"dry_run"`
	Rows       map[string]int64 `json:"rows"`
	Error      string           `json:"error,omitempty"`
}

func main() {
	start := time.Now()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "FATAL: DATABASE_URL não setada")
		os.Exit(2)
	}
	dryRun := os.Getenv("DRY_RUN") == "1"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
		Rows:      map[string]int64{},
	}

	if dryRun {
		if err := runDryCount(ctx, pool, &rep); err != nil {
			rep.Error = err.Error()
		}
	} else {
		if err := runCleanup(ctx, pool, &rep); err != nil {
			rep.Error = err.Error()
		}
	}

	rep.DurationMs = time.Since(start).Milliseconds()

	if err := json.NewEncoder(os.Stdout).Encode(rep); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: encode JSON: %v\n", err)
		os.Exit(2)
	}

	writeTextfileMetrics(rep)

	if rep.Error != "" {
		fmt.Fprintf(os.Stderr, "test-cleanup: ERRO: %s\n", rep.Error)
		os.Exit(1)
	}

	var total int64
	for _, v := range rep.Rows {
		if v > 0 {
			total += v
		}
	}
	if dryRun {
		fmt.Fprintf(os.Stderr, "test-cleanup [DRY_RUN]: %d row(s) candidatas em %dms\n",
			total, rep.DurationMs)
	} else {
		fmt.Fprintf(os.Stderr, "test-cleanup: %d row(s) removidas em %dms\n",
			total, rep.DurationMs)
	}
}

// runCleanup executa o cleanup em TX atômica. Ordem topológica:
// filhas primeiro (refresh_tokens, profiles, etc.) → orders pending →
// users por último.
//
// NOTA: orders status IN ('paid','confirmed') são EXPLICITAMENTE preservadas.
// Smoke nunca confirma um pagamento (gateway externo não chama webhook em
// `*@viralefy.test`), mas se alguma race fizer isso acontecer, reconcile
// detecta e operador investiga manual.
func runCleanup(parent context.Context, pool *pgxpool.Pool, rep *report) error {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Subquery reusada: ids de test users elegíveis (idade > 2h).
	// Em todos os DELETEs abaixo passamos pattern + age como params $1, $2.
	// Inline pra evitar criar VIEW temporária.
	userIDsSubquery := `
		SELECT id FROM users
		 WHERE email LIKE $1
		   AND created_at < NOW() - INTERVAL '` + cleanupAgeWindow + `'
	`

	run := func(label, sql string, args ...any) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			// Tabela inexistente em DBs muito antigos: ignora.
			if strings.Contains(err.Error(), "42P01") ||
				strings.Contains(err.Error(), "does not exist") {
				rep.Rows[label] = -1
				return nil
			}
			return fmt.Errorf("%s: %w", label, err)
		}
		rep.Rows[label] = tag.RowsAffected()
		return nil
	}

	// Ordem importante por FKs:
	//   orders.profile_id → profiles.id (RESTRICT) → orders antes de profiles
	//   orders.user_id    → users.id              → orders antes de users
	//   orders            → preservar paid/confirmed (não esperado, mas safe)
	//
	// Estratégia: deletar orders NÃO-pagos dos test users PRIMEIRO (libera FK
	// pra profiles + users). Orders paid/confirmed permanecem → user também
	// permanece (filtro NOT IN no DELETE users). Reconcile pega esse caso.

	// --- 1) Refresh tokens (auth fixtures).
	if err := run("refresh_tokens", `
		DELETE FROM refresh_tokens
		 WHERE user_id IN (`+userIDsSubquery+`)`,
		testEmailPattern); err != nil {
		return err
	}

	// --- 2) User events / journeys (tracking analytics).
	if err := run("user_events", `
		DELETE FROM user_events
		 WHERE user_id IN (`+userIDsSubquery+`)`,
		testEmailPattern); err != nil {
		return err
	}
	if err := run("user_journeys", `
		DELETE FROM user_journeys
		 WHERE user_id IN (`+userIDsSubquery+`)`,
		testEmailPattern); err != nil {
		return err
	}

	// --- 3) Email events (notification fixtures).
	if err := run("email_events", `
		DELETE FROM email_events
		 WHERE email LIKE $1`,
		testEmailPattern); err != nil {
		return err
	}

	// --- 4) Orders NÃO-pagos dos test users (libera FK pra profiles/users).
	//        Orders paid/confirmed preservadas pra investigação manual
	//        (cenário não esperado — gateway externo não confirma `.test`).
	if err := run("orders_unpaid_delete", `
		DELETE FROM orders
		 WHERE user_id IN (`+userIDsSubquery+`)
		   AND status NOT IN ('paid','confirmed')`,
		testEmailPattern); err != nil {
		return err
	}

	// --- 5) Profiles criadas pelo new_profile do smoke. Agora a FK do
	//        orders.profile_id já foi liberada pelo step 4.
	if err := run("profiles", `
		DELETE FROM profiles
		 WHERE user_id IN (`+userIDsSubquery+`)`,
		testEmailPattern); err != nil {
		return err
	}

	// --- 6) Por último: users — APENAS se não tiverem orders confirmadas.
	//        Test users com pagamento confirmado (cenário não esperado)
	//        ficam preservados pra investigação manual.
	if err := run("users", `
		DELETE FROM users
		 WHERE email LIKE $1
		   AND created_at < NOW() - INTERVAL '`+cleanupAgeWindow+`'
		   AND id NOT IN (
		     SELECT user_id FROM orders
		      WHERE user_id IS NOT NULL
		        AND status IN ('paid','confirmed')
		   )`,
		testEmailPattern); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// runDryCount é a versão observe-only: conta candidatos sem mutar nada.
// Útil pra inspecionar antes de habilitar timer pela primeira vez.
func runDryCount(ctx context.Context, pool *pgxpool.Pool, rep *report) error {
	queries := []struct {
		label string
		sql   string
	}{
		{"users_candidates", `
			SELECT COUNT(*) FROM users
			 WHERE email LIKE $1
			   AND created_at < NOW() - INTERVAL '` + cleanupAgeWindow + `'`},
		{"orders_pending_candidates", `
			SELECT COUNT(*) FROM orders
			 WHERE user_id IN (
			   SELECT id FROM users WHERE email LIKE $1
			     AND created_at < NOW() - INTERVAL '` + cleanupAgeWindow + `'
			 ) AND status='pending'`},
		{"orders_paid_preserved", `
			SELECT COUNT(*) FROM orders
			 WHERE user_id IN (
			   SELECT id FROM users WHERE email LIKE $1
			 ) AND status IN ('paid','confirmed')`},
	}
	for _, q := range queries {
		var n int64
		if err := pool.QueryRow(ctx, q.sql, testEmailPattern).Scan(&n); err != nil {
			if strings.Contains(err.Error(), "42P01") {
				rep.Rows[q.label] = -1
				continue
			}
			return fmt.Errorf("%s: %w", q.label, err)
		}
		rep.Rows[q.label] = n
	}
	return nil
}

// writeTextfileMetrics emite contadores .prom pro node_exporter. Mesmo
// padrão write-then-rename do user-deletion-cron.
func writeTextfileMetrics(rep report) {
	path := os.Getenv("TEXTFILE_PATH")
	if path == "" {
		path = defaultTextfilePath
	}
	if path == "-" {
		return
	}

	var sb strings.Builder
	sb.WriteString("# HELP viralefy_test_cleanup_rows_total Rows afetadas pelo cleanup de fixtures @viralefy.test na última passagem (por tabela).\n")
	sb.WriteString("# TYPE viralefy_test_cleanup_rows_total counter\n")
	for label, n := range rep.Rows {
		if n < 0 {
			continue // skipped (tabela inexistente)
		}
		fmt.Fprintf(&sb, "viralefy_test_cleanup_rows_total{table=%q} %d\n", label, n)
	}

	sb.WriteString("# HELP viralefy_test_cleanup_last_run_timestamp_seconds Unix timestamp da última execução.\n")
	sb.WriteString("# TYPE viralefy_test_cleanup_last_run_timestamp_seconds gauge\n")
	fmt.Fprintf(&sb, "viralefy_test_cleanup_last_run_timestamp_seconds %d\n", time.Now().Unix())

	sb.WriteString("# HELP viralefy_test_cleanup_error Última execução teve erro fatal (1=sim, 0=não).\n")
	sb.WriteString("# TYPE viralefy_test_cleanup_error gauge\n")
	if rep.Error != "" {
		fmt.Fprintf(&sb, "viralefy_test_cleanup_error 1\n")
	} else {
		fmt.Fprintf(&sb, "viralefy_test_cleanup_error 0\n")
	}

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
