package domain

import (
	"context"
	"time"
)

// TwoFASecret representa o estado 2FA persistido por principal (admin OU
// user — repo separado por tabela, mas shape idêntico). enrolled_at NULL
// significa "secret gerado mas verificação inicial ainda não passou" —
// permite re-enroll sem deixar 2FA half-on. last_used_at é só auditoria.
type TwoFASecret struct {
	PrincipalID       string
	SecretEncrypted   string    // hex(AES-256-GCM(secret_base32))
	BackupCodesHashed []string  // bcrypt(plain_code) — comparison constant-time
	EnrolledAt        *time.Time
	LastUsedAt        *time.Time
}

// TwoFARepository abstrai persistência. Implementação concreta por
// principal (admin_2fa / user_2fa) — duas tabelas, mesmo contrato.
type TwoFARepository interface {
	Get(ctx context.Context, principalID string) (*TwoFASecret, error)
	// Upsert grava (insert ou update) com enrolled_at=NULL inicial. O
	// service marca enrolled_at depois do primeiro Verify ok.
	Upsert(ctx context.Context, s TwoFASecret) error
	// MarkEnrolled seta enrolled_at = NOW(). Chamado após Verify ok.
	MarkEnrolled(ctx context.Context, principalID string) error
	// MarkUsed seta last_used_at = NOW(). Chamado em cada login bem-sucedido.
	MarkUsed(ctx context.Context, principalID string) error
	// ConsumeBackupCode tenta consumir um backup code. Compara contra cada
	// hash em backup_codes_hashed; match → remove do array + retorna true.
	// Use one-time: backup code não pode ser reutilizado.
	ConsumeBackupCode(ctx context.Context, principalID, code string) (bool, error)
	// Delete remove a row (disable 2FA). Para admin obrigatório, o handler
	// deve recusar exceto por superadmin override.
	Delete(ctx context.Context, principalID string) error
}
