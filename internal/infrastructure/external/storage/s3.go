// Package storage implementa a porta application.ObjectStorage com cliente
// S3-compatível (minio-go). Funciona tanto com MinIO local quanto com
// Cloudflare R2 — a diferença é só endpoint + useSSL no Config.
//
// Por que minio-go: tem ~3MB, sem deps gigantes (aws-sdk-go-v2 traz ~50MB
// de modules). API igualmente Go-idiomática.
package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/config"
)

// S3Client é o adapter S3-compat de application.ObjectStorage.
// Inicializado uma vez no main e injetado em Handlers.
type S3Client struct {
	c    *minio.Client
	cfg  config.StorageConfig
}

// New conecta no endpoint S3-compat. Não cria buckets aqui — assume que o
// instalador (mc init) ou um restore drill já criou viralefy-proofs e
// viralefy-public. Bucket missing vira erro no primeiro Put.
func New(cfg config.StorageConfig) (*S3Client, error) {
	if !cfg.Enabled() {
		return nil, fmt.Errorf("storage config disabled (Endpoint=%q AccessKey=empty=%v)",
			cfg.Endpoint, cfg.AccessKey == "")
	}
	c, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}
	return &S3Client{c: c, cfg: cfg}, nil
}

// Put grava o conteúdo no bucket. ContentType vai pro Content-Type response
// header quando o cliente baixa via presigned URL — importante pro browser
// renderizar img/* inline em vez de baixar como octet-stream.
func (s *S3Client) Put(
	ctx context.Context, bucket, key string, body io.Reader, size int64, contentType string,
) (string, error) {
	bucket = s.resolveBucket(bucket)
	_, err := s.c.PutObject(ctx, bucket, key, body, size, minio.PutObjectOptions{
		ContentType: contentType,
		// CacheControl public 1h: proofs raramente mudam (são imutáveis na prática),
		// mas mantemos curto porque admin pode regenerar presigned URL frequente.
		CacheControl: "private, max-age=3600",
	})
	if err != nil {
		return "", fmt.Errorf("put %s/%s: %w", bucket, key, err)
	}
	return key, nil
}

// PresignedGetURL gera URL temporária. Backoffice abre direto na browser do
// admin (curto-circuita o proxy do API). expiry > 7d retorna erro (limite S3).
func (s *S3Client) PresignedGetURL(
	ctx context.Context, bucket, key string, expiry time.Duration,
) (string, error) {
	if expiry > 7*24*time.Hour {
		return "", fmt.Errorf("presigned URL expiry capped at 7 days (s3 limit)")
	}
	if expiry <= 0 {
		expiry = 5 * time.Minute
	}
	bucket = s.resolveBucket(bucket)
	u, err := s.c.PresignedGetObject(ctx, bucket, key, expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("presign %s/%s: %w", bucket, key, err)
	}
	return u.String(), nil
}

func (s *S3Client) Delete(ctx context.Context, bucket, key string) error {
	bucket = s.resolveBucket(bucket)
	return s.c.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}

// resolveBucket aceita um alias semântico ("proofs"/"public") ou o nome
// raw. Service layer chama com alias pra ficar agnóstico do bucket name
// (que pode mudar entre prod e dev sem code change).
func (s *S3Client) resolveBucket(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "proofs":
		return s.cfg.BucketProofs
	case "public":
		return s.cfg.BucketPublic
	}
	return name
}

// Assert na interface pra catch refactor em compile-time.
var _ application.ObjectStorage = (*S3Client)(nil)
