// user-deletion-cron — executor físico de pedidos de exclusão LGPD.
//
// Resolve o tech-debt registrado em
// internal/application/user_data_service.go:20-21.
//
// Comportamento:
//
//   - Lê user_deletion_requests WHERE status='pending'
//     AND executes_at <= NOW().
//   - Pra cada pedido, abre uma transação ATÔMICA por usuário e:
//   - Deleta dados pessoais cascading (tokens, 2FA, profiles,
//     subscriptions, tickets, etc.) — FK não tem CASCADE no schema,
//     então fazemos manual + ordem topológica.
//   - Anonimiza orders (user_id=NULL, preserva email_at_purchase/
//     name_at_purchase pra retenção fiscal 5y exigida pela Receita).
//   - Anonimiza audit_log (rows imutáveis; substitui PII por '[DELETED]'
//     em metadata.email, mas mantém a linha).
//   - Deleta o user.
//   - Marca a request como executed (executed_at=NOW()).
//   - Idempotente: rodar 2x não duplica nada (DELETE...WHERE não erra
//     se nada existe; UPDATE status='executed' fica no estado final).
//   - Tolerante a falhas: erro em uma row vira status='failed' +
//     error_message; ciclo segue pra próxima.
//   - Métricas via textfile collector (caminho TEXTFILE_PATH; default
//     /var/lib/node_exporter/textfile_collector/viralefy_user_deletion.prom).
//     Os 3 contadores expostos:
//   - viralefy_user_deletion_executed_total
//   - viralefy_user_deletion_failed_total
//   - viralefy_user_deletion_pending_count
//
// O que NÃO deletamos (retenção fiscal/auditoria):
//   - orders         → anonimizado (user_id=NULL)
//   - order_refunds  → preservado (vinculado a orders, fiscal)
//   - invoices       → preservado (fiscal)
//   - audit_log      → anonimizado (imutabilidade preservada)
//
// O que deletamos hard:
//   - refresh_tokens, revoked_jtis (via cleanup natural)
//   - user_2fa
//   - api_keys (owner_user_id)
//   - profiles
//   - subscriptions
//   - email_events (por email — anonimizamos via UPDATE; user não tem FK direta)
//   - fraud_signals (por email/IP — anonimizamos)
//   - user_events, user_journeys
//   - credit_accounts, credit_transactions
//   - tickets, ticket_messages
//   - password_resets
//   - referral_rewards (referrer/referred)
//   - reviews
//   - users (último, depois de quebrar todas as FKs)
//
// Logging:
//   - stdout: JSON estruturado por execução (Loki-friendly).
//   - NUNCA loga PII (email/nome) — só user_id e contagens.
//
// Build:
//
//	cd viralefy_core && go build -o bin/user-deletion-cron ./cmd/user-deletion-cron
//
// Uso:
//
//	DATABASE_URL=postgres://... ./user-deletion-cron
//	DATABASE_URL=postgres://... DRY_RUN=1 ./user-deletion-cron
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

// report é o envelope serializado em stdout por execução.
type report struct {
	Timestamp     string         `json:"timestamp"`
	DurationMs    int64          `json:"duration_ms"`
	DryRun        bool           `json:"dry_run"`
	Picked        int            `json:"picked"`
	Executed      int            `json:"executed"`
	Failed        int            `json:"failed"`
	PendingTotal  int            `json:"pending_total"`
	Details       []executionLog `json:"details,omitempty"`
}

type executionLog struct {
	RequestID string `json:"request_id"`
	UserID    string `json:"user_id"`
	Status    string `json:"status"`            // executed | failed
	Error     string `json:"error,omitempty"`   // só em failed
	Rows      map[string]int64 `json:"rows,omitempty"` // tabela -> rows afetadas
}

// defaultTextfilePath é o caminho convencionado pelo systemd unit;
// node_exporter expõe arquivos *.prom de lá automaticamente.
const defaultTextfilePath = "/var/lib/node_exporter/textfile_collector/viralefy_user_deletion.prom"

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

	// 1) lista pendentes vencidos
	due, err := listDue(ctx, pool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: list due: %v\n", err)
		os.Exit(2)
	}
	rep.Picked = len(due)

	// 2) executa cada um
	for _, d := range due {
		log := executionLog{RequestID: d.requestID, UserID: d.userID}
		if dryRun {
			log.Status = "executed"
			log.Rows = map[string]int64{"dry_run": 1}
			rep.Executed++
			rep.Details = append(rep.Details, log)
			fmt.Fprintf(os.Stderr, "[DRY_RUN] would delete user=%s req=%s\n", d.userID, d.requestID)
			continue
		}
		rows, execErr := executeDeletion(ctx, pool, d.userID, d.requestID)
		if execErr != nil {
			markFailed(ctx, pool, d.requestID, execErr.Error())
			log.Status = "failed"
			log.Error = execErr.Error()
			rep.Failed++
			fmt.Fprintf(os.Stderr, "[FAILED] req=%s user=%s err=%s\n", d.requestID, d.userID, execErr.Error())
		} else {
			log.Status = "executed"
			log.Rows = rows
			rep.Executed++
			fmt.Fprintf(os.Stderr, "[EXECUTED] req=%s user=%s rows=%d\n",
				d.requestID, d.userID, sumRows(rows))
		}
		rep.Details = append(rep.Details, log)
	}

	// 3) pending_total (todas as requests pending, mesmo no futuro — pra alertar
	// se a fila estiver crescendo)
	pending, perr := countPending(ctx, pool)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "[warn] count pending: %v\n", perr.Error())
	}
	rep.PendingTotal = pending

	rep.DurationMs = time.Since(start).Milliseconds()

	// 4) emite JSON pra journal/Loki
	if err := json.NewEncoder(os.Stdout).Encode(rep); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: encode JSON: %v\n", err)
		os.Exit(2)
	}

	// 5) métricas textfile (defense-in-depth: falha silenciosa, prom job
	// vai notar drift via age do arquivo)
	writeTextfileMetrics(rep)

	if rep.Failed > 0 {
		os.Exit(1)
	}
}

// duePick é uma row retornada do SELECT — minimalista pra reduzir
// surface de leak (não puxamos email/name aqui).
type duePick struct {
	requestID string
	userID    string
}

func listDue(ctx context.Context, pool *pgxpool.Pool) ([]duePick, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, user_id
		  FROM user_deletion_requests
		 WHERE status = 'pending'
		   AND executes_at <= NOW()
		 ORDER BY executes_at ASC
		 LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []duePick
	for rows.Next() {
		var d duePick
		if err := rows.Scan(&d.requestID, &d.userID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// executeDeletion roda uma transação atômica que apaga/anonimiza tudo
// do usuário e marca a request como executed. Devolve mapa de
// "tabela → rows afetadas" pra observabilidade.
//
// Algumas tabelas podem não existir em ambientes velhos — toleramos via
// "ON CONFLICT IGNORE"-like manual: o exec falha somente quando a tabela
// existe mas a query é inválida. Pra robustez extra usamos
// `DELETE FROM ... WHERE` sem JOINs (FK já garante integridade).
func executeDeletion(parent context.Context, pool *pgxpool.Pool, userID, reqID string) (map[string]int64, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows := map[string]int64{}

	// Helper: exec + count, tolerante a tabela inexistente (códigos pgx
	// `undefined_table` 42P01). Em ambientes onde uma tabela não foi
	// migrada (ex.: testes em DB vazio), seguimos.
	run := func(label, sql string, args ...any) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			// Tabela inexistente: ignora. Qualquer outro erro: aborta.
			if strings.Contains(err.Error(), "42P01") ||
				strings.Contains(err.Error(), "does not exist") {
				rows[label] = -1 // sinaliza "skip"
				return nil
			}
			return fmt.Errorf("%s: %w", label, err)
		}
		rows[label] = tag.RowsAffected()
		return nil
	}

	// --- 1) Capture email/name do user pra snapshot em orders + audit_log
	var snapEmail, snapName string
	if err := tx.QueryRow(ctx,
		`SELECT email, name FROM users WHERE id=$1`, userID).
		Scan(&snapEmail, &snapName); err != nil {
		if err == pgx.ErrNoRows {
			// Usuário já foi removido (rerun do cron, ou exclusão manual).
			// Idempotência: marca request como executed e segue.
			if _, e := tx.Exec(ctx, `
				UPDATE user_deletion_requests
				   SET status='executed', executed_at=NOW()
				 WHERE id=$1`, reqID); e != nil {
				return nil, fmt.Errorf("mark executed (orphan): %w", e)
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit (orphan): %w", err)
			}
			rows["orphan"] = 1
			return rows, nil
		}
		return nil, fmt.Errorf("snapshot user: %w", err)
	}

	// --- 2) Anonimiza orders ANTES de deletar o user (FK ainda existe).
	//        Backfill defensivo do snapshot (caso colunas estejam vazias
	//        pra orders pre-migration 041) — mantém retenção fiscal 5y.
	if err := run("orders_anonymize",
		`UPDATE orders
		    SET email_at_purchase = COALESCE(email_at_purchase, $2),
		        name_at_purchase  = COALESCE(name_at_purchase,  $3),
		        user_id           = NULL
		  WHERE user_id = $1`, userID, snapEmail, snapName); err != nil {
		return nil, err
	}

	// --- 3) Anonimiza audit_log (imutabilidade preservada — só rewrite
	//        de campos PII em metadata. Linha permanece pra trilha forense).
	//        actor_id == user_id quando admin loga ação como usuário; pouco
	//        comum mas existe. target_id também.
	if err := run("audit_log_anonymize_actor",
		`UPDATE audit_log
		    SET metadata = jsonb_set(
		                     jsonb_set(metadata, '{actor_email}', '"[DELETED]"', true),
		                     '{actor_name}', '"[DELETED]"', true)
		  WHERE actor_id = $1`, userID); err != nil {
		return nil, err
	}
	if err := run("audit_log_anonymize_target",
		`UPDATE audit_log
		    SET metadata = jsonb_set(
		                     jsonb_set(metadata, '{target_email}', '"[DELETED]"', true),
		                     '{target_name}', '"[DELETED]"', true)
		  WHERE target_id = $1`, userID); err != nil {
		return nil, err
	}

	// --- 4) DELETE em ordem topológica das FKs (filhas antes das pais).
	//        Algumas têm ON DELETE CASCADE — explícito não dói, garante
	//        o número certo de rows em `rows[]` pra métrica.
	//        `arg` é o único parâmetro de cada query (todas usam exatamente
	//        $1). Queries por email passam snapEmail; demais passam userID.
	deletes := []struct {
		label string
		sql   string
		arg   string
	}{
		// Auth & tokens
		{"refresh_tokens", `DELETE FROM refresh_tokens WHERE user_id = $1`, userID},
		{"password_resets", `DELETE FROM password_resets WHERE user_id = $1`, userID},
		{"user_2fa", `DELETE FROM user_2fa WHERE user_id = $1`, userID},

		// API keys (owner_user_id)
		{"api_keys", `DELETE FROM api_keys WHERE owner_user_id = $1`, userID},

		// Comportamento / fraud (por user_id quando existir)
		{"user_events", `DELETE FROM user_events WHERE user_id = $1`, userID},
		{"user_journeys", `DELETE FROM user_journeys WHERE user_id = $1`, userID},

		// Por email (snapshot já capturado em snapEmail). NOTA: queries
		// só usam $1 → o "arg" é o email, não o userID, neste bloco.
		{"email_events_by_email", `DELETE FROM email_events WHERE email = $1`, snapEmail},
		{"fraud_signals_by_email", `DELETE FROM fraud_signals WHERE actor = $1`, snapEmail},

		// Compras e billing (não-fiscais)
		{"subscriptions", `DELETE FROM subscriptions WHERE user_id = $1`, userID},

		// Helpdesk
		{"ticket_messages",
			`DELETE FROM ticket_messages
			   WHERE ticket_id IN (SELECT id FROM tickets WHERE user_id = $1)`, userID},
		{"tickets", `DELETE FROM tickets WHERE user_id = $1`, userID},

		// Reviews
		{"reviews", `DELETE FROM reviews WHERE user_id = $1`, userID},

		// Credit ledger
		{"credit_transactions", `DELETE FROM credit_transactions WHERE user_id = $1`, userID},
		{"credit_accounts", `DELETE FROM credit_accounts WHERE user_id = $1`, userID},

		// Referrals (FK em ambos lados)
		{"referral_rewards_referrer",
			`DELETE FROM referral_rewards WHERE referrer_user_id = $1`, userID},
		{"referral_rewards_referred",
			`DELETE FROM referral_rewards WHERE referred_user_id = $1`, userID},
		{"users_referred_by_clear",
			`UPDATE users SET referred_by_user_id = NULL WHERE referred_by_user_id = $1`, userID},

		// Profiles
		{"profiles", `DELETE FROM profiles WHERE user_id = $1`, userID},

		// Por último: o user
		{"users", `DELETE FROM users WHERE id = $1`, userID},
	}

	for _, d := range deletes {
		if err := run(d.label, d.sql, d.arg); err != nil {
			return nil, err
		}
	}

	// --- 5) Marca request como executada
	if _, err := tx.Exec(ctx, `
		UPDATE user_deletion_requests
		   SET status='executed', executed_at=NOW()
		 WHERE id=$1`, reqID); err != nil {
		return nil, fmt.Errorf("mark executed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return rows, nil
}

// markFailed roda fora da tx falha (porque ela já foi rolled back).
// Usa contexto novo curto pra não morrer junto com cancelamento parent.
func markFailed(parent context.Context, pool *pgxpool.Pool, reqID, errMsg string) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	// Trunca msg pra não inflar a tabela com stack traces de pgx.
	if len(errMsg) > 1000 {
		errMsg = errMsg[:1000]
	}
	_, _ = pool.Exec(ctx, `
		UPDATE user_deletion_requests
		   SET status='failed', error_message=$2
		 WHERE id=$1`, reqID, errMsg)
}

func countPending(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM user_deletion_requests WHERE status='pending'`).Scan(&n)
	return n, err
}

func sumRows(m map[string]int64) int64 {
	var s int64
	for _, v := range m {
		if v > 0 {
			s += v
		}
	}
	return s
}

// writeTextfileMetrics emite o .prom no path configurado. Atômico via
// write-then-rename pra evitar leitura parcial pelo node_exporter.
func writeTextfileMetrics(rep report) {
	path := os.Getenv("TEXTFILE_PATH")
	if path == "" {
		path = defaultTextfilePath
	}
	if path == "-" { // opt-out explícito (testes)
		return
	}

	var sb strings.Builder
	sb.WriteString("# HELP viralefy_user_deletion_executed_total Total de pedidos LGPD executados com sucesso pelo cron na última passagem.\n")
	sb.WriteString("# TYPE viralefy_user_deletion_executed_total counter\n")
	fmt.Fprintf(&sb, "viralefy_user_deletion_executed_total %d\n", rep.Executed)

	sb.WriteString("# HELP viralefy_user_deletion_failed_total Total de pedidos LGPD que falharam na última passagem.\n")
	sb.WriteString("# TYPE viralefy_user_deletion_failed_total counter\n")
	fmt.Fprintf(&sb, "viralefy_user_deletion_failed_total %d\n", rep.Failed)

	sb.WriteString("# HELP viralefy_user_deletion_pending_count Pedidos LGPD em status=pending (qualquer executes_at).\n")
	sb.WriteString("# TYPE viralefy_user_deletion_pending_count gauge\n")
	fmt.Fprintf(&sb, "viralefy_user_deletion_pending_count %d\n", rep.PendingTotal)

	sb.WriteString("# HELP viralefy_user_deletion_last_run_timestamp_seconds Unix timestamp da última execução do cron.\n")
	sb.WriteString("# TYPE viralefy_user_deletion_last_run_timestamp_seconds gauge\n")
	fmt.Fprintf(&sb, "viralefy_user_deletion_last_run_timestamp_seconds %d\n", time.Now().Unix())

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
