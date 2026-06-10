// migrate-proofs — one-shot offline migrator que move comprovantes legados
// armazenados como data:URL base64 dentro de orders.proof_url para o bucket
// MinIO/R2 viralefy-proofs, gravando a chave canônica em
// orders.proof_storage_key. Não toca proof_url (rollback fica seguro).
//
// Funcionamento:
//
//	1. Carrega config padrão do app (.env via env vars).
//	2. Itera em batches de --batch (default 50) rows:
//	     proof_url LIKE 'data:%' AND proof_storage_key IS NULL
//	   Ordena por created_at ASC pra processar comprovantes mais antigos
//	   primeiro (drena fila histórica em vez de pular pra recentes).
//	3. Pra cada row:
//	     a. Parse data:URL → (mime, bytes). MIME detectado do header E
//	        validado contra magic bytes (anti-MIME-spoof). Caso mismatch,
//	        confia nas magic bytes. Falha gera skip + log warn (não aborta
//	        o batch).
//	     b. Determina extensão (.jpg/.png/.webp/.gif/.pdf).
//	     c. PUT no bucket "proofs" com key proofs/<order_id>.<ext>.
//	        ContentType = MIME real. Tamanho = len(bytes).
//	     d. Verifica upload via StatObject (Hash de tamanho >0 confirma
//	        que o objeto realmente existe no bucket).
//	     e. UPDATE orders SET proof_storage_key=<key> WHERE id=<order_id>.
//	4. Métricas finais: migrated, skipped, failed, total bytes uploaded.
//
// Idempotência:
//   - WHERE proof_storage_key IS NULL pula rows já migradas (rerun seguro
//     após pane parcial).
//   - PutObject sobrescreve sem erro (MinIO/R2 não tem flag "no-overwrite"
//     por default; mesma key sempre vence).
//
// Não roda em prod sem flag explícito. --dry-run lista o que faria.
// --execute confirma a intenção.
//
// Build:
//
//	cd viralefy_core && go build -o bin/migrate-proofs ./cmd/migrate-proofs
//
// Uso (após backup do DB):
//
//	./bin/migrate-proofs --execute --batch=50 --limit=0
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Viralefy/viralefy_core/internal/config"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/storage"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// allowedExt mapeia MIME validado → extensão a usar na key. Conservador:
// só formatos que o handler de upload já aceita (allowedProofMIME).
var allowedExt = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/webp":      ".webp",
	"image/gif":       ".gif",
	"application/pdf": ".pdf",
}

func main() {
	var (
		batch   = flag.Int("batch", 50, "rows por iteração (lock curto, commit incremental)")
		limit   = flag.Int("limit", 0, "total máximo de rows a migrar (0 = sem limite)")
		dryRun  = flag.Bool("dry-run", false, "só lista o que faria, sem PUT/UPDATE")
		execute = flag.Bool("execute", false, "confirma a intenção de mexer no prod (obrigatório fora de --dry-run)")
	)
	flag.Parse()

	if !*dryRun && !*execute {
		fmt.Fprintln(os.Stderr, "ERRO: passe --dry-run pra preview ou --execute pra rodar de verdade.")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if !cfg.Storage.Enabled() {
		log.Fatal("storage config disabled (STORAGE_ENDPOINT/ACCESS_KEY vazios) — abortando")
	}

	// Sinal Ctrl-C interrompe o loop entre batches (não no meio de um
	// upload). Migrador é idempotente, então parar é seguro.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer db.Close()

	s3, err := storage.New(cfg.Storage)
	if err != nil {
		log.Fatalf("storage init: %v", err)
	}

	// Acesso ao minio.Client cru pra StatObject (não exposto na porta
	// application.ObjectStorage de propósito — só o migrador precisa).
	mc, err := minio.New(cfg.Storage.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.Storage.AccessKey, cfg.Storage.SecretKey, ""),
		Secure: cfg.Storage.UseSSL,
		Region: cfg.Storage.Region,
	})
	if err != nil {
		log.Fatalf("minio raw client: %v", err)
	}

	logger.Info("starting migrate-proofs",
		"batch", *batch,
		"limit", *limit,
		"dry_run", *dryRun,
		"endpoint", cfg.Storage.Endpoint,
		"bucket", cfg.Storage.BucketProofs)

	stats := runMigration(ctx, logger, db, s3, mc, cfg.Storage.BucketProofs, *batch, *limit, *dryRun)

	logger.Info("done",
		"migrated", stats.migrated,
		"skipped", stats.skipped,
		"failed", stats.failed,
		"bytes_uploaded", stats.bytesUploaded,
		"duration", stats.duration.String())

	if stats.failed > 0 {
		os.Exit(1)
	}
}

type runStats struct {
	migrated      int
	skipped       int
	failed        int
	bytesUploaded int64
	duration      time.Duration
}

// runMigration loop principal. Sai quando: batch retorna 0 rows, atinge
// --limit ou contexto é cancelado.
func runMigration(
	ctx context.Context,
	logger *slog.Logger,
	db *postgres.DB,
	s3 *storage.S3Client,
	mc *minio.Client,
	bucket string,
	batchSize, hardLimit int,
	dryRun bool,
) runStats {
	start := time.Now()
	var stats runStats

	for {
		if ctx.Err() != nil {
			logger.Warn("context cancelled, stopping loop", "error", ctx.Err().Error())
			break
		}
		if hardLimit > 0 && stats.migrated+stats.skipped+stats.failed >= hardLimit {
			logger.Info("hard limit reached, stopping", "limit", hardLimit)
			break
		}

		rows, err := fetchPendingProofs(ctx, db, batchSize)
		if err != nil {
			logger.Error("fetch batch failed", "error", err.Error())
			stats.failed++
			break
		}
		if len(rows) == 0 {
			logger.Info("no pending proofs left")
			break
		}

		for _, row := range rows {
			if ctx.Err() != nil {
				break
			}
			outcome := migrateOne(ctx, logger, db, s3, mc, bucket, row, dryRun)
			switch outcome.kind {
			case outcomeMigrated:
				stats.migrated++
				stats.bytesUploaded += int64(outcome.bytes)
			case outcomeSkipped:
				stats.skipped++
			case outcomeFailed:
				stats.failed++
			}
		}

		// Em --dry-run o WHERE da próxima query continua matchando os
		// mesmos rows (proof_storage_key não é atualizado). Sai depois
		// do primeiro batch pra não loop infinito.
		if dryRun {
			logger.Info("dry-run: stopping after first batch to avoid infinite loop")
			break
		}
	}

	stats.duration = time.Since(start)
	return stats
}

type pendingRow struct {
	id       string
	proofURL string
}

func fetchPendingProofs(ctx context.Context, db *postgres.DB, batch int) ([]pendingRow, error) {
	// LIMIT $1 + ORDER BY created_at ASC drena fila histórica primeiro.
	// Filtra por prefixo "data:" — não toca rows com proof_url http(s)
	// (legacy externos tipo imgur) nem rows que já têm storage_key.
	rows, err := db.Pool().Query(ctx, `
		SELECT id, proof_url
		  FROM orders
		 WHERE proof_storage_key IS NULL
		   AND proof_url IS NOT NULL
		   AND proof_url LIKE 'data:%'
		 ORDER BY created_at ASC
		 LIMIT $1`, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingRow
	for rows.Next() {
		var r pendingRow
		if err := rows.Scan(&r.id, &r.proofURL); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type outcomeKind int

const (
	outcomeMigrated outcomeKind = iota
	outcomeSkipped
	outcomeFailed
)

type outcome struct {
	kind  outcomeKind
	bytes int
}

func migrateOne(
	ctx context.Context,
	logger *slog.Logger,
	db *postgres.DB,
	s3 *storage.S3Client,
	mc *minio.Client,
	bucket string,
	row pendingRow,
	dryRun bool,
) outcome {
	mime, payload, err := parseDataURL(row.proofURL)
	if err != nil {
		logger.Warn("parse data url failed", "order_id", row.id, "error", err.Error())
		return outcome{kind: outcomeSkipped}
	}
	// Magic-byte sniff: confirma o que o cabeçalho data:URL disse.
	// Antiga API podia ter gravado image/jpeg quando era PNG (cliente
	// mentiu no Content-Type). Confiamos no que os bytes dizem.
	detected := detectMIME(payload)
	if detected != "" && detected != mime {
		logger.Warn("mime mismatch, using detected",
			"order_id", row.id, "header", mime, "detected", detected)
		mime = detected
	}
	ext, ok := allowedExt[mime]
	if !ok {
		logger.Warn("mime not in allowlist, skipping", "order_id", row.id, "mime", mime)
		return outcome{kind: outcomeSkipped}
	}
	key := "proofs/" + row.id + ext

	if dryRun {
		logger.Info("dry-run would upload",
			"order_id", row.id,
			"mime", mime,
			"key", key,
			"bytes", len(payload))
		return outcome{kind: outcomeMigrated, bytes: len(payload)}
	}

	// Upload. ContentType vai pro header do objeto pra browser renderizar
	// inline quando o admin abre presigned URL.
	_, err = s3.Put(ctx, "proofs", key, bytes.NewReader(payload), int64(len(payload)), mime)
	if err != nil {
		logger.Error("put failed", "order_id", row.id, "key", key, "error", err.Error())
		return outcome{kind: outcomeFailed}
	}

	// Verifica re-lendo metadata: Stat retorna size + etag se existe.
	// Diff de tamanho indicaria upload parcial (cortado por timeout/proxy).
	stat, err := mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		logger.Error("stat after put failed", "order_id", row.id, "key", key, "error", err.Error())
		return outcome{kind: outcomeFailed}
	}
	if stat.Size != int64(len(payload)) {
		logger.Error("size mismatch after put",
			"order_id", row.id, "key", key,
			"expected", len(payload), "got", stat.Size)
		return outcome{kind: outcomeFailed}
	}

	// UPDATE direto (sem ir pelo repo) pra evitar dep do domain layer
	// e manter o migrador isolado. Idempotente: WHERE proof_storage_key
	// IS NULL evita corrida com app rodando.
	tag, err := db.Pool().Exec(ctx, `
		UPDATE orders
		   SET proof_storage_key=$2, updated_at=NOW()
		 WHERE id=$1 AND proof_storage_key IS NULL`, row.id, key)
	if err != nil {
		logger.Error("update proof_storage_key failed",
			"order_id", row.id, "key", key, "error", err.Error())
		return outcome{kind: outcomeFailed}
	}
	if tag.RowsAffected() == 0 {
		// Outro processo migrou no meio tempo (improvável em offline,
		// mas defensivo). Conta como skipped.
		logger.Info("row already migrated by another process",
			"order_id", row.id, "key", key)
		return outcome{kind: outcomeSkipped}
	}
	logger.Info("migrated",
		"order_id", row.id,
		"key", key,
		"mime", mime,
		"bytes", len(payload))
	return outcome{kind: outcomeMigrated, bytes: len(payload)}
}

// parseDataURL decodifica strings tipo "data:image/png;base64,iVBOR..."
// Aceita só base64-encoded (cliente que gravou URL-encoded em proof_url
// não é cenário esperado). Retorna MIME normalizado + bytes crus.
func parseDataURL(s string) (string, []byte, error) {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return "", nil, fmt.Errorf("not a data URL")
	}
	rest := s[len(prefix):]
	commaIdx := strings.Index(rest, ",")
	if commaIdx < 0 {
		return "", nil, fmt.Errorf("missing comma in data URL")
	}
	meta := rest[:commaIdx]
	payload := rest[commaIdx+1:]

	// meta = "<mime>;base64" ou "<mime>" ou ";base64". Default text/plain
	// se vazio — mas isso não é caso esperado pra proofs.
	mime := "text/plain"
	isBase64 := false
	for _, part := range strings.Split(meta, ";") {
		p := strings.TrimSpace(strings.ToLower(part))
		switch {
		case p == "base64":
			isBase64 = true
		case p == "":
			// ignore
		default:
			mime = p
		}
	}
	if !isBase64 {
		// proof_url legado pode ter sido URL-encoded em vez de base64.
		// Decode percent-escapes e devolve cru — improvável mas defensivo.
		dec, err := url.QueryUnescape(payload)
		if err != nil {
			return mime, nil, fmt.Errorf("data url not base64 and percent-decode failed: %w", err)
		}
		return mime, []byte(dec), nil
	}
	// Padding pode estar ausente em base64 raw. Tenta std primeiro, raw
	// fallback. Linebreaks dentro do base64 (rare em data:URL mas
	// possível) são tolerados via StdEncoding.WithPadding(StdPadding).
	cleaned := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(payload)
	if b, err := base64.StdEncoding.DecodeString(cleaned); err == nil {
		return mime, b, nil
	}
	b, err := base64.RawStdEncoding.DecodeString(cleaned)
	if err != nil {
		return mime, nil, fmt.Errorf("base64 decode: %w", err)
	}
	return mime, b, nil
}

// detectMIME aplica magic-byte sniffing pros formatos da whitelist. Retorna
// "" quando os primeiros bytes não batem com nenhum (não confunde com
// "não sei" — caller cai no MIME do header se "" devolvido).
func detectMIME(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(b) >= 4 && bytes.Equal(b[:4], []byte("%PDF")):
		return "application/pdf"
	}
	return ""
}

