package application

import (
	"context"
	"io"
	"time"
)

// ObjectStorage é a porta de saída pra storage S3-compatível.
// Implementação concreta vive em infrastructure/external/storage.
// Quando Endpoint vazio (config.Storage.Enabled()=false), main injeta
// um NoopStorage que retorna erro em todo write/read — o sistema cai no
// fluxo legado de base64 inline pra back-compat durante a migração.
type ObjectStorage interface {
	// Put grava bytes num bucket com a key informada. contentType vai pro
	// header (image/png, application/pdf, etc.). Retorna a key efetiva
	// (mesmo da input, mas confirmada após write).
	Put(ctx context.Context, bucket, key string, body io.Reader, size int64, contentType string) (string, error)
	// PresignedGetURL gera URL temporária pra download direto pelo cliente.
	// expiry máximo 7 dias (limite S3). Usado pelo backoffice pra mostrar
	// proof ao admin sem precisar passar pelo proxy do API.
	PresignedGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
	// Delete remove um objeto. Best-effort: chamado em order delete,
	// erro vira warn no log (não derruba o delete cascading).
	Delete(ctx context.Context, bucket, key string) error
}

// NoopStorage é o fallback quando storage não está configurado. Mantém
// o API rodando em ambientes que ainda não tem MinIO/R2 setado. Trocar
// pra MinIO quando deploy 7.1 ativar.
type NoopStorage struct{}

func (NoopStorage) Put(context.Context, string, string, io.Reader, int64, string) (string, error) {
	return "", ErrStorageDisabled
}
func (NoopStorage) PresignedGetURL(context.Context, string, string, time.Duration) (string, error) {
	return "", ErrStorageDisabled
}
func (NoopStorage) Delete(context.Context, string, string) error {
	return ErrStorageDisabled
}

// ErrStorageDisabled — sinal pro handler cair no fluxo legado (base64
// inline) em vez de retornar 500 pro cliente.
var ErrStorageDisabled = errStorageDisabled{}

type errStorageDisabled struct{}

func (errStorageDisabled) Error() string { return "object storage not configured" }
