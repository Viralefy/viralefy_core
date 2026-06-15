package application

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/totp"
)

func decodeHex(s string) ([]byte, error) { return hex.DecodeString(s) }
func decodeB64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// BackupCodeCost é o cost factor do bcrypt para os hashes de backup codes
// de 2FA. Backup codes são senha-equivalentes (1 código = bypass total do
// segundo fator), então usa o mesmo cost da senha (12). Hashes antigos com
// cost 10 continuam validando — o cost vive embedded no próprio hash, então
// só re-enroll/regenerate produz hash com cost 12.
const BackupCodeCost = 12

// TwoFAService orquestra enroll + verify + consume backup codes.
// Não conhece "admin vs user" — opera sobre uma TwoFARepository abstrata.
// Main wire-up cria duas instâncias (uma por tabela).
type TwoFAService struct {
	repo   domain.TwoFARepository
	encKey []byte // 32 bytes — TWOFA_ENCRYPTION_KEY
}

func NewTwoFAService(repo domain.TwoFARepository, encKey []byte) *TwoFAService {
	return &TwoFAService{repo: repo, encKey: encKey}
}

// EnrollResult carrega o que o usuário vê no setup wizard — apenas UMA vez.
// secret_base32 e backup_codes não voltam mais; admin perdeu → recovery via
// superadmin reset.
type EnrollResult struct {
	SecretBase32 string   `json:"secret_base32"`
	OTPAuthURL   string   `json:"otpauth_url"`     // pro QR code
	BackupCodes  []string `json:"backup_codes"`    // plain text ONE TIME only
}

// Enroll gera secret + 8 backup codes. Persiste cifrado + hashed.
// enrolled_at fica NULL até Verify primeira passar. Re-enroll antes de
// verify sobrescreve (caso usuário fechou a tela sem terminar).
func (s *TwoFAService) Enroll(ctx context.Context, principalID, accountLabel string) (*EnrollResult, error) {
	secretBase32, otpURL, err := totp.Enroll(accountLabel)
	if err != nil {
		return nil, err
	}
	encSecret, err := totp.Encrypt(secretBase32, s.encKey)
	if err != nil {
		return nil, err
	}
	codes, err := totp.GenerateBackupCodes(8)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(codes))
	for _, c := range codes {
		h, err := bcrypt.GenerateFromPassword([]byte(c), BackupCodeCost)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, string(h))
	}
	if err := s.repo.Upsert(ctx, domain.TwoFASecret{
		PrincipalID:       principalID,
		SecretEncrypted:   encSecret,
		BackupCodesHashed: hashes,
	}); err != nil {
		return nil, err
	}
	return &EnrollResult{
		SecretBase32: secretBase32,
		OTPAuthURL:   otpURL,
		BackupCodes:  codes,
	}, nil
}

// Verify checa um código TOTP (6 dígitos) OU um backup code (10 chars).
// Em ambos os casos, atualiza last_used_at. Verify do primeiro código após
// enroll marca enrolled_at=NOW (ativa o 2FA pra logins futuros).
//
// Heurística: 6 chars = TOTP; outros = backup. Match strict; sem fallback
// silencioso entre os dois (evita enumeration timing).
func (s *TwoFAService) Verify(ctx context.Context, principalID, code string) error {
	code = strings.TrimSpace(strings.ToUpper(code))
	if code == "" {
		return domain.ErrInvalidInput
	}
	secret, err := s.repo.Get(ctx, principalID)
	if err != nil {
		return err
	}
	wasEnrolled := secret.EnrolledAt != nil

	if isTOTPShape(code) {
		plain, err := totp.Decrypt(secret.SecretEncrypted, s.encKey)
		if err != nil {
			return err
		}
		if !totp.Verify(plain, code) {
			return domain.ErrUnauthorized
		}
	} else {
		ok, err := s.repo.ConsumeBackupCode(ctx, principalID, code)
		if err != nil {
			return err
		}
		if !ok {
			return domain.ErrUnauthorized
		}
	}
	if !wasEnrolled {
		_ = s.repo.MarkEnrolled(ctx, principalID)
	}
	_ = s.repo.MarkUsed(ctx, principalID)
	return nil
}

// IsEnrolled retorna true sse o principal já completou o primeiro Verify
// (enrolled_at != NULL). Usado pelo login flow pra decidir partial_token.
func (s *TwoFAService) IsEnrolled(ctx context.Context, principalID string) bool {
	secret, err := s.repo.Get(ctx, principalID)
	if err != nil || secret == nil {
		return false
	}
	return secret.EnrolledAt != nil
}

// Disable remove 2FA. Service não distingue "admin obrigatório vs opcional"
// — handler é quem decide se pode chamar (admin: só via superadmin reset).
func (s *TwoFAService) Disable(ctx context.Context, principalID string) error {
	return s.repo.Delete(ctx, principalID)
}

// isTOTPShape é uma heurística: 6 dígitos numéricos = TOTP. backup code
// é 10 chars alfanuméricos base32 (A-Z + 2-7). Evita ambiguidade.
func isTOTPShape(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParseEncryptionKey valida e converte a string da env (hex 64 chars OR
// base64 44 chars) pra []byte 32. Aceita ambos os formatos comuns —
// instaladores variados.
func ParseEncryptionKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("TWOFA_ENCRYPTION_KEY empty")
	}
	// Tenta hex 64 chars (32 bytes).
	if len(s) == 64 {
		if b, err := decodeHex(s); err == nil {
			return b, nil
		}
	}
	// Tenta base64 (44 chars com padding, ~43 sem).
	if b, err := decodeB64(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("TWOFA_ENCRYPTION_KEY must be 32 bytes (hex 64 chars or base64 44 chars), got %d chars", len(s))
}
