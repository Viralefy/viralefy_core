package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// apiKeyPrefix — ajuda triagem visual em logs ("vf_live_..."). É só prefixo
// cosmético; entropia real vem dos 32 bytes random aleatorizados.
const apiKeyPrefix = "vf_live_"

// apiKeyRandomBytes — 32 bytes = 256 bits de entropia. Base32 sem padding
// dá 52 chars; com prefixo total ~60 chars.
const apiKeyRandomBytes = 32

// APIKeyService gerencia o ciclo de vida das credenciais B2B.
//
// Geração: 32 bytes random -> base32 (sem padding, upper) -> "vf_live_<...>".
// Persistência: sha256(plain) em hex. Plain NUNCA volta a aparecer depois
// do Create — o front mostra UMA vez no modal e o usuário guarda.
type APIKeyService struct {
	repo domain.APIKeyRepository
}

func NewAPIKeyService(repo domain.APIKeyRepository) *APIKeyService {
	return &APIKeyService{repo: repo}
}

// hashKey é o SHA-256 hex do plain. Determinístico → mesmo plain sempre
// resolve pra mesma row em GetByHash.
func hashKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func generatePlainKey() (string, error) {
	buf := make([]byte, apiKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	body := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return apiKeyPrefix + body, nil
}

// CreateResult agrupa o que o handler precisa: o model (sem segredo) +
// a key plain que deve ser exibida apenas no response da criação.
type CreateResult struct {
	APIKey domain.APIKey `json:"api_key"`
	// Key é o plain que o cliente deve guardar. Não persiste; mostrado UMA vez.
	Key string `json:"key"`
}

// Create gera, hasha e persiste uma nova API key vinculada ao userID.
// Retorna o plain UMA vez via CreateResult.Key — caller deve manter no
// response e o front exibe em modal com warning.
func (s *APIKeyService) Create(ctx context.Context, userID, label string) (*CreateResult, error) {
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, domain.ErrInvalidInput
	}
	if len(label) > 80 {
		label = label[:80]
	}
	plain, err := generatePlainKey()
	if err != nil {
		return nil, err
	}
	model := domain.APIKey{
		ID:          uuid.New().String(),
		Label:       label,
		OwnerUserID: userID,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.repo.Create(ctx, model, hashKey(plain)); err != nil {
		return nil, err
	}
	return &CreateResult{APIKey: model, Key: plain}, nil
}

// ValidateKey é o caminho quente: middleware chama em todo request /v2.
// Devolve o APIKey ativo e dispara MarkUsed async (não bloqueia request).
//
// Erros:
//   - "" key → ErrUnauthorized (header ausente normaliza pra isso no middleware)
//   - hash não encontrado / revogada → ErrUnauthorized (não vazar ErrNotFound
//     pro cliente — só "unauthorized")
func (s *APIKeyService) ValidateKey(ctx context.Context, plainKey string) (*domain.APIKey, error) {
	plainKey = strings.TrimSpace(plainKey)
	if plainKey == "" {
		return nil, domain.ErrUnauthorized
	}
	k, err := s.repo.GetByHash(ctx, hashKey(plainKey))
	if err != nil {
		// Repo devolve ErrNotFound quando hash desconhecido ou revogado.
		// Normaliza pra 401 — não distinguir os dois evita enumeration.
		return nil, domain.ErrUnauthorized
	}
	// MarkUsed best-effort — não bloqueia o request.
	go func(id string) {
		ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.MarkUsed(ctx2, id)
	}(k.ID)
	return k, nil
}

// RevokeKey marca a key como revogada. Apenas o dono pode revogar
// (filtrado no repo via WHERE owner_user_id).
func (s *APIKeyService) RevokeKey(ctx context.Context, userID, keyID string) error {
	if userID == "" {
		return domain.ErrUnauthorized
	}
	if keyID == "" {
		return domain.ErrInvalidInput
	}
	return s.repo.Revoke(ctx, userID, keyID)
}

// ListMyKeys devolve as keys do user (ativas e revogadas) em ordem
// decrescente de criação. Nunca devolve key plain — só metadados.
func (s *APIKeyService) ListMyKeys(ctx context.Context, userID string) ([]domain.APIKey, error) {
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	return s.repo.ListByUser(ctx, userID)
}
