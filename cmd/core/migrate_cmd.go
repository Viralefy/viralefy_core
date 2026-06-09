package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Viralefy/viralefy_core/internal/config"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// runMigrateCmd despacha `viralefy-api migrate <sub>`. Sem args ou args
// inválidos imprime ajuda + exit 2.
//
// Implementação: subcomandos curtos, sem deps externas (cobra/cli). Os
// 4 subcomandos cobrem o ciclo completo:
//
//	status   — read-only, mostra cada migration + estado
//	up       — aplica pendentes (mesmo que o boot faz)
//	backfill — marca todas como aplicadas SEM rodar SQL. Usar UMA vez em
//	           prod existente quando o tracker é introduzido (caso atual).
//	version  — print da migration mais recente aplicada
func runMigrateCmd() {
	if len(os.Args) < 3 {
		printMigrateHelp()
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer db.Close()

	switch os.Args[2] {
	case "status":
		cmdMigrateStatus(ctx, db)
	case "up":
		cmdMigrateUp(ctx, db)
	case "backfill":
		cmdMigrateBackfill(ctx, db)
	case "version":
		cmdMigrateVersion(ctx, db)
	default:
		printMigrateHelp()
		os.Exit(2)
	}
}

func printMigrateHelp() {
	fmt.Fprintln(os.Stderr, "Usage: viralefy-api migrate <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  status     List all migrations and their applied state")
	fmt.Fprintln(os.Stderr, "  up         Apply pending migrations (same as automatic boot)")
	fmt.Fprintln(os.Stderr, "  backfill   Mark all migrations as applied WITHOUT running SQL.")
	fmt.Fprintln(os.Stderr, "             Use ONCE on existing prod when introducing the tracker.")
	fmt.Fprintln(os.Stderr, "  version    Print the latest applied migration version")
}

func cmdMigrateStatus(ctx context.Context, db *postgres.DB) {
	list, err := postgres.ListMigrations(ctx, db)
	if err != nil {
		log.Fatalf("list migrations: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tNAME\tSTATUS\tAPPLIED AT")
	pending := 0
	mismatch := 0
	for _, m := range list {
		status := "pending"
		applied := "-"
		switch {
		case m.Mismatch:
			status = "MISMATCH"
			mismatch++
			applied = m.AppliedAt.Format("2006-01-02 15:04")
		case m.Applied:
			status = "applied"
			applied = m.AppliedAt.Format("2006-01-02 15:04")
		default:
			pending++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.Version, m.Name, status, applied)
	}
	tw.Flush()
	fmt.Printf("\nTotal: %d  Pending: %d  Mismatch: %d\n", len(list), pending, mismatch)
	if mismatch > 0 {
		fmt.Println("\n⚠️  MISMATCH significa que o arquivo .sql foi editado APÓS ter sido aplicado.")
		fmt.Println("   Isso é proibido pelo tracker. Crie uma migration nova em vez de editar a antiga.")
		os.Exit(1)
	}
}

func cmdMigrateUp(ctx context.Context, db *postgres.DB) {
	before, _ := postgres.ListMigrations(ctx, db)
	pendingBefore := countPending(before)
	if err := postgres.RunMigrations(ctx, db); err != nil {
		log.Fatalf("migrate up: %v", err)
	}
	after, _ := postgres.ListMigrations(ctx, db)
	pendingAfter := countPending(after)
	applied := pendingBefore - pendingAfter
	fmt.Printf("ok — %d migration(s) applied (was pending: %d, now pending: %d)\n",
		applied, pendingBefore, pendingAfter)
}

func cmdMigrateBackfill(ctx context.Context, db *postgres.DB) {
	if err := postgres.BackfillMigrations(ctx, db); err != nil {
		log.Fatalf("backfill: %v", err)
	}
	list, _ := postgres.ListMigrations(ctx, db)
	applied := 0
	for _, m := range list {
		if m.Applied {
			applied++
		}
	}
	fmt.Printf("ok — %d migrations marked as applied (no SQL executed)\n", applied)
}

func cmdMigrateVersion(ctx context.Context, db *postgres.DB) {
	list, err := postgres.ListMigrations(ctx, db)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	var latest *postgres.MigrationStatus
	for i := range list {
		if list[i].Applied {
			latest = &list[i]
		}
	}
	if latest == nil {
		fmt.Println("no migrations applied yet")
		return
	}
	fmt.Printf("%s_%s (applied %s)\n",
		latest.Version, latest.Name, latest.AppliedAt.Format(time.RFC3339))
}

func countPending(list []postgres.MigrationStatus) int {
	n := 0
	for _, m := range list {
		if !m.Applied {
			n++
		}
	}
	return n
}

// runSeedCmd roda Seed() explicitamente. Pra ser chamado SOMENTE quando
// preciso (criar DB do zero, recover de wipe). Não é mais executado em
// todo boot — o seed só é "primer" do schema inicial.
func runSeedCmd() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer db.Close()
	if err := postgres.Seed(ctx, db); err != nil {
		log.Fatalf("seed: %v", err)
	}
	fmt.Println("ok — seed completed (idempotent / DO NOTHING on conflict)")
}
