package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration tracker estilo Laravel/Rails/Flyway.
//
// PROBLEMA original (2026-06-09 incidente "marketplace items voltam"):
// RunMigrations rodava TODOS os .up.sql em todo boot. Migrations com INSERT
// (ex.: 010 inserindo categorias bms_facebook/perfis_redes/emails_validados)
// ressuscitavam rows que admin tinha apagado. Cliente apaga em prod, deploy
// roda, items voltam — loop infinito.
//
// SOLUÇÃO: tabela schema_migrations rastreia o que já rodou. Pra cada
// arquivo:
//
//	1. parseia version (prefixo numérico do nome, ex.: "010")
//	2. computa SHA256 do conteúdo
//	3. SELECT FROM schema_migrations WHERE version=$1
//	   - existe E checksum bate → SKIP (já rodou)
//	   - existe E checksum NÃO bate → ERRO (alguém editou migration já
//	     aplicada; alterar fato consumado não é seguro, log + abort)
//	   - não existe → BEGIN; apply; INSERT into schema_migrations; COMMIT
//
// Migrations rodam em ordem lexicográfica do nome — 001 antes de 002, etc.
//
// Idempotência: rodar 2x não muda nada. Editar migration nova (ainda não
// aplicada) é OK; editar migration já aplicada é bloqueio explícito.

const migrationsTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version     TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	checksum    TEXT NOT NULL,
	applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	duration_ms BIGINT NOT NULL DEFAULT 0
);`

// migrationFileRe captura "NNN_qualquer_coisa.up.sql" → version="NNN",
// name="qualquer_coisa". Aceita 3+ dígitos pra suportar 1000+ migrations.
var migrationFileRe = regexp.MustCompile(`^(\d{3,})_(.+)\.up\.sql$`)

type migrationEntry struct {
	Version  string
	Name     string
	Filename string
	Content  string
	Checksum string
}

// loadMigrations lê /migrations/*.up.sql, parseia version+name, computa
// checksum, e devolve ordenado por version (lexicográfica suficiente
// porque versions têm padding zero — 010 > 009).
func loadMigrations() ([]migrationEntry, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	out := make([]migrationEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		raw, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		out = append(out, migrationEntry{
			Version:  m[1],
			Name:     m[2],
			Filename: e.Name(),
			Content:  string(raw),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// RunMigrations aplica migrations pendentes em ordem. Idempotente.
//
// Em prod com schema já existente (deploy onde a tabela schema_migrations
// não existe ainda), faz auto-backfill: se detecta DB com schema legado
// mas sem tracker, assume que TODAS migrations existentes já foram
// aplicadas via o RunMigrations antigo (idempotente, sem tracking) e
// só marca como tal sem re-rodar.
//
// Heurística de "prod existente sem tracker":
//   - schema_migrations recém-criada (0 rows) E
//   - tabela `users` já existe (significa que migrations rodaram antes)
//
// Sem essa heurística, qualquer prod legado quebraria no primeiro deploy
// porque o tracker tentaria re-aplicar 38 migrations e algumas têm INSERT
// que conflitaria com state já existente.
func RunMigrations(ctx context.Context, db *DB) error {
	if _, err := db.pool.Exec(ctx, migrationsTableDDL); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	if err := autoBackfillIfLegacy(ctx, db); err != nil {
		return fmt.Errorf("auto-backfill: %w", err)
	}
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migs {
		applied, dbChecksum, err := isMigrationApplied(ctx, db, m.Version)
		if err != nil {
			return fmt.Errorf("check %s: %w", m.Version, err)
		}
		if applied {
			if dbChecksum != m.Checksum {
				// Migration já aplicada foi editada — não é seguro re-aplicar
				// (poderia recriar coisas deletadas, mudar tipos com dados
				// já presentes, etc). Erro explícito força revisão humana.
				return fmt.Errorf(
					"migration %s_%s checksum mismatch (db=%s, file=%s) — migration foi editada após aplicação, isso é proibido. Crie uma migration nova em vez de editar a antiga, ou faça backfill via SQL manual",
					m.Version, m.Name, dbChecksum[:12], m.Checksum[:12],
				)
			}
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("apply %s_%s: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// isMigrationApplied retorna (applied, checksum_stored, error).
func isMigrationApplied(ctx context.Context, db *DB, version string) (bool, string, error) {
	var checksum string
	err := db.pool.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version=$1`, version).Scan(&checksum)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return false, "", nil
		}
		return false, "", err
	}
	return true, checksum, nil
}

// applyMigration roda a migration dentro de uma transação e grava no
// schema_migrations no mesmo tx. Sem AUTOCOMMIT/no-tx — se quebra no meio,
// rollback total e nenhum side-effect.
func applyMigration(ctx context.Context, db *DB, m migrationEntry) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback após commit é no-op

	start := time.Now()
	if _, err := tx.Exec(ctx, m.Content); err != nil {
		return fmt.Errorf("exec sql: %w", err)
	}
	dur := time.Since(start).Milliseconds()
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, duration_ms) VALUES ($1,$2,$3,$4)`,
		m.Version, m.Name, m.Checksum, dur,
	); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	return tx.Commit(ctx)
}

// autoBackfillIfLegacy detecta o cenário "prod já tem schema mas o tracker
// nunca rodou" e marca todas as migrations atuais como aplicadas sem
// executá-las. Roda UMA única vez na primeira boot pós-deploy do tracker.
//
// Heurística:
//   - schema_migrations existe com 0 rows (acabou de ser criada agora)
//   - tabela `users` existe (== migrations já rodaram pelo método antigo)
//
// Sem ambas condições, no-op — DB realmente vazio (dev/staging novo) cai
// no caminho normal e roda todas as migrations do zero.
func autoBackfillIfLegacy(ctx context.Context, db *DB) error {
	var n int
	if err := db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil // tracker já em uso
	}
	var hasUsers bool
	if err := db.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'users'
		)`).Scan(&hasUsers); err != nil {
		return err
	}
	if !hasUsers {
		return nil // DB realmente vazio — segue caminho normal de fresh-install
	}
	// Prod legado: marca tudo como aplicado sem rodar.
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migs {
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO schema_migrations (version, name, checksum)
			VALUES ($1, $2, $3)
			ON CONFLICT (version) DO NOTHING`,
			m.Version, m.Name, m.Checksum,
		); err != nil {
			return err
		}
	}
	return nil
}

// BackfillMigrations marca TODAS as migrations atuais como aplicadas SEM
// rodá-las. Uso UMA VEZ em prod existente, antes de habilitar o tracker
// novo — garante que migrations que já foram executadas via RunMigrations
// antigo não rodem de novo. Idempotente: se schema_migrations já tem rows,
// só preenche os que faltam.
func BackfillMigrations(ctx context.Context, db *DB) error {
	if _, err := db.pool.Exec(ctx, migrationsTableDDL); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migs {
		_, err := db.pool.Exec(ctx, `
			INSERT INTO schema_migrations (version, name, checksum)
			VALUES ($1, $2, $3)
			ON CONFLICT (version) DO NOTHING`,
			m.Version, m.Name, m.Checksum,
		)
		if err != nil {
			return fmt.Errorf("backfill %s: %w", m.Version, err)
		}
	}
	return nil
}

// MigrationStatus é o estado de uma migration p/ a CLI.
type MigrationStatus struct {
	Version   string
	Name      string
	Applied   bool
	AppliedAt time.Time
	Mismatch  bool // file checksum ≠ db checksum
}

// ListMigrations devolve o estado de todas as migrations conhecidas.
// Usado pelo CLI viralefy-migrate status.
func ListMigrations(ctx context.Context, db *DB) ([]MigrationStatus, error) {
	if _, err := db.pool.Exec(ctx, migrationsTableDDL); err != nil {
		return nil, err
	}
	migs, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	out := make([]MigrationStatus, 0, len(migs))
	for _, m := range migs {
		st := MigrationStatus{Version: m.Version, Name: m.Name}
		var dbChecksum string
		err := db.pool.QueryRow(ctx,
			`SELECT checksum, applied_at FROM schema_migrations WHERE version=$1`,
			m.Version,
		).Scan(&dbChecksum, &st.AppliedAt)
		if err != nil {
			if !strings.Contains(err.Error(), "no rows") {
				return nil, err
			}
		} else {
			st.Applied = true
			st.Mismatch = dbChecksum != m.Checksum
		}
		out = append(out, st)
	}
	return out, nil
}
