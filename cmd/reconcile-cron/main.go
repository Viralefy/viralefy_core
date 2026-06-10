// reconcile-cron — verificador diário de invariantes do negócio.
//
// Roda 15 queries SQL READ-ONLY contra prod DB. Cada query é uma sentinela
// que deve retornar ZERO rows em estado saudável; qualquer count > 0 sinaliza
// drift entre o estado real e os invariantes assumidos pelo modelo.
//
// Saída:
//   - stdout: JSON estruturado (consumido pelo Loki via journal/promtail).
//   - stderr: linha human-readable por finding pra ler no journalctl.
//   - exit 0 quando todos invariantes OK (zero drifts).
//   - exit 1 quando há drift (timer marca falha + opcional notificação).
//
// Defense-in-depth:
//   - Read-only: nenhuma query muta state. Constraint enforçada socialmente
//     (todas as queries são SELECTs) — não há transação de escrita aberta.
//   - Idempotente: rodar 2x dá mesmo resultado (DB é só observado).
//   - Tolerante a falhas: erro numa query não derruba as demais; finding
//     vira "error" no JSON e ciclo continua.
//   - Timeout total: 5min (contexto cancela queries pendentes).
//   - Sem deps externas além de pgx + std lib.
//
// Notificação:
//   - Se ADMIN_WEBHOOK_URL setada e há drifts, faz POST JSON (Slack/Discord
//     compatível — formato `{"text": "...", "drifts": [...]}`).
//   - Sem URL = skipa silenciosamente; output continua via stdout.
//
// Uso:
//
//	DATABASE_URL=postgres://... ./reconcile-cron
//	DATABASE_URL=postgres://... ADMIN_WEBHOOK_URL=https://... ./reconcile-cron
//
// Build:
//
//	cd viralefy_core && go build -o bin/reconcile-cron ./cmd/reconcile-cron
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// invariant descreve uma query sentinela. Esperado retornar 0 rows.
type invariant struct {
	// name é o slug usado no JSON (snake_case, prefixado por tabela).
	name string
	// severity classifica impact:
	//   high   — corrupção monetária ou auth (precisa ação imediata).
	//   medium — anomalia estrutural (FK órfã, índice fora do esperado).
	//   low    — limpeza/cleanup pendente (não bloqueia, só lembra ops).
	severity string
	// description é human-readable, aparece no log + webhook.
	description string
	// query SELECT que retorna até `sampleLimit` IDs/refs. Primeira coluna
	// = id pro sample, segunda coluna (opcional) = count global. Se só
	// uma coluna vier, count = len(rows).
	query string
}

const sampleLimit = 10

// invariants é a lista canônica. ADICIONAR aqui = ganha drift detection.
// Order importa só visualmente — não há dependência entre elas.
var invariants = []invariant{
	{
		name:        "orders_paid_no_external_ref",
		severity:    "high",
		description: "Orders status=paid SEM external_ref em gateway EXTERNO (gateway não confirmou ou link quebrado). Exclui providers manuais (manual_pix, manual_*) e pagamentos por créditos — nesses fluxos external_ref é nulo por design.",
		query: `
			SELECT o.id FROM orders o
			LEFT JOIN payment_gateways pg ON pg.id = o.gateway_id
			WHERE o.status = 'paid'
			  AND o.payment_method = 'gateway'
			  AND (o.external_ref IS NULL OR o.external_ref = '')
			  AND (pg.provider IS NULL OR pg.provider NOT LIKE 'manual_%')
			ORDER BY o.created_at DESC
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "orders_paid_no_paid_at_via_invoice",
		severity:    "medium",
		description: "Invoices status=paid SEM paid_at preenchido (timestamp perdido).",
		query: `
			SELECT id FROM invoices
			WHERE status = 'paid' AND paid_at IS NULL
			ORDER BY updated_at DESC
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "refresh_tokens_orphan_user",
		severity:    "medium",
		description: "refresh_tokens.user_id apontando pra user inexistente (FK CASCADE quebrou).",
		query: `
			SELECT rt.id FROM refresh_tokens rt
			LEFT JOIN users u ON u.id = rt.user_id
			WHERE rt.user_id IS NOT NULL AND u.id IS NULL
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "refresh_tokens_orphan_admin",
		severity:    "medium",
		description: "refresh_tokens.admin_id apontando pra admin inexistente.",
		query: `
			SELECT rt.id FROM refresh_tokens rt
			LEFT JOIN admins a ON a.id = rt.admin_id
			WHERE rt.admin_id IS NOT NULL AND a.id IS NULL
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "revoked_jtis_expired_still_present",
		severity:    "low",
		description: "revoked_jtis com expires_at < NOW() - 7d ainda no hot-set (cleanup atrasado).",
		query: `
			SELECT jti FROM revoked_jtis
			WHERE expires_at < NOW() - INTERVAL '7 days'
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "credit_accounts_negative_balance",
		severity:    "high",
		description: "credit_accounts.balance_cents < 0 (impossível pelo modelo — bug de débito).",
		query: `
			SELECT user_id FROM credit_accounts
			WHERE balance_cents < 0
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "credit_ledger_balance_mismatch",
		severity:    "high",
		description: "SUM(credit_transactions.amount_cents) != credit_accounts.balance_cents (cache fora do ledger).",
		query: `
			SELECT ca.user_id
			FROM credit_accounts ca
			JOIN (
				SELECT user_id, COALESCE(SUM(amount_cents), 0) AS total
				FROM credit_transactions
				GROUP BY user_id
			) ct ON ct.user_id = ca.user_id
			WHERE ca.balance_cents <> ct.total
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "plans_active_without_price",
		severity:    "high",
		description: "Plans active=true com price_cents <= 0 (impossível cobrar, bug de UX/billing).",
		query: `
			SELECT id FROM plans
			WHERE active = true AND price_cents <= 0
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "plans_active_no_active_gateway",
		severity:    "high",
		description: "Plans active=true mas NENHUM payment_gateway active disponível (catálogo invendível).",
		query: `
			SELECT p.id FROM plans p
			WHERE p.active = true
			  AND NOT EXISTS (SELECT 1 FROM payment_gateways WHERE active = true)
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "orders_pending_over_7d",
		severity:    "low",
		description: "Orders status=pending criadas há mais de 7 dias (zumbis — provavelmente abandono).",
		query: `
			SELECT id FROM orders
			WHERE status = 'pending'
			  AND created_at < NOW() - INTERVAL '7 days'
			ORDER BY created_at ASC
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "gateways_duplicate_active_provider",
		severity:    "high",
		description: "Mais de um payment_gateway active pro mesmo provider (config conflito → cobrança imprevisível).",
		query: `
			SELECT provider FROM payment_gateways
			WHERE active = true
			GROUP BY provider
			HAVING COUNT(*) > 1
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "orders_refund_over_amount",
		severity:    "high",
		description: "Orders com refunded_usd_cents > amount_cents (overrefund — perda monetária).",
		query: `
			SELECT id FROM orders
			WHERE refunded_usd_cents > amount_cents
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "orders_refund_sum_mismatch",
		severity:    "high",
		description: "Orders.refunded_usd_cents != SUM(order_refunds.refund_usd_cents) (cache fora do ledger de refunds).",
		query: `
			SELECT o.id
			FROM orders o
			JOIN (
				SELECT order_id, SUM(refund_usd_cents) AS total
				FROM order_refunds
				GROUP BY order_id
			) r ON r.order_id = o.id
			WHERE o.refunded_usd_cents <> r.total
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "subscriptions_active_no_next_billing",
		severity:    "medium",
		description: "Subscriptions status=active com next_billing_at no passado > 24h (cron de renovação parado?).",
		query: `
			SELECT id FROM subscriptions
			WHERE status = 'active'
			  AND next_billing_at < NOW() - INTERVAL '24 hours'
			LIMIT ` + sampleLimitStr,
	},
	{
		name:        "coupons_used_count_negative_or_overflow",
		severity:    "medium",
		description: "Coupons com used_count > max_uses (não devia ser possível — race no checkout).",
		query: `
			SELECT code FROM coupons
			WHERE max_uses IS NOT NULL AND used_count > max_uses
			LIMIT ` + sampleLimitStr,
	},
}

// sampleLimitStr é a constante embutida nas queries SQL (fmt.Sprintf
// poluiria o template — preferimos build-time string concat).
const sampleLimitStr = "10"

// finding é o resultado de uma invariant rodada.
type finding struct {
	Name        string   `json:"name"`
	Severity    string   `json:"severity"`
	Description string   `json:"description"`
	Count       int      `json:"count"`
	SampleIDs   []string `json:"sample_ids,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// report é o envelope serializado em stdout.
type report struct {
	Timestamp         string    `json:"timestamp"`
	DurationMs        int64     `json:"duration_ms"`
	InvariantsChecked int       `json:"invariants_checked"`
	Drifts            []finding `json:"drifts"`
	Errors            int       `json:"errors"`
}

func main() {
	start := time.Now()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "FATAL: DATABASE_URL não setada")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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

	var drifts []finding
	errCount := 0
	for _, inv := range invariants {
		f := runInvariant(ctx, pool, inv)
		if f.Error != "" {
			errCount++
			fmt.Fprintf(os.Stderr, "[ERROR] %s: %s\n", inv.name, f.Error)
		}
		if f.Count > 0 || f.Error != "" {
			drifts = append(drifts, f)
			if f.Error == "" {
				fmt.Fprintf(os.Stderr,
					"[DRIFT %s] %s: %d row(s) — sample: %v\n",
					f.Severity, f.Name, f.Count, f.SampleIDs,
				)
			}
		}
	}

	rep := report{
		Timestamp:         start.UTC().Format(time.RFC3339),
		DurationMs:        time.Since(start).Milliseconds(),
		InvariantsChecked: len(invariants),
		Drifts:            drifts,
		Errors:            errCount,
	}

	// stdout: JSON única linha (Loki/journal-friendly).
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(rep); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: encode JSON: %v\n", err)
		os.Exit(2)
	}

	// Notificação opt-in.
	if len(drifts) > 0 {
		notifyIfConfigured(rep)
		fmt.Fprintf(os.Stderr,
			"reconcile-cron: %d drift(s) detectado(s), %d erro(s), %dms\n",
			len(drifts), errCount, rep.DurationMs,
		)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr,
		"reconcile-cron: all %d invariants OK, %dms\n",
		rep.InvariantsChecked, rep.DurationMs,
	)
}

// runInvariant executa uma query e materializa finding. Não propaga erro:
// erro vira f.Error pra não derrubar o ciclo (defense-in-depth).
func runInvariant(parent context.Context, pool *pgxpool.Pool, inv invariant) finding {
	// Sub-timeout 30s por query — garante que uma query lenta não engole
	// o orçamento total de 5min.
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	f := finding{
		Name:        inv.name,
		Severity:    inv.severity,
		Description: inv.description,
	}

	rows, err := pool.Query(ctx, inv.query)
	if err != nil {
		f.Error = err.Error()
		return f
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			f.Error = "scan: " + err.Error()
			return f
		}
		f.SampleIDs = append(f.SampleIDs, id)
	}
	if err := rows.Err(); err != nil {
		f.Error = "rows: " + err.Error()
		return f
	}
	f.Count = len(f.SampleIDs)
	return f
}

// notifyIfConfigured posta o report num webhook genérico (Slack/Discord
// compatível). Falha silenciosamente — webhook flaky NÃO deve fazer o
// cron falhar (já reportamos via exit 1 + journal).
func notifyIfConfigured(rep report) {
	url := os.Getenv("ADMIN_WEBHOOK_URL")
	if url == "" {
		return
	}

	// Constrói payload com `text` (Slack-friendly) + estrutura completa
	// (parsers tipo Discord ignoram campos extras).
	summary := fmt.Sprintf(
		"viralefy-reconcile: %d drift(s) detectado(s) em %s",
		len(rep.Drifts), rep.Timestamp,
	)
	for _, d := range rep.Drifts {
		summary += fmt.Sprintf("\n• [%s] %s: %d row(s)", d.Severity, d.Name, d.Count)
	}

	payload := map[string]any{
		"text":   summary,
		"report": rep,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[warn] webhook post falhou: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "[warn] webhook status=%d\n", resp.StatusCode)
	}
}
