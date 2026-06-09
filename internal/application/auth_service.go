package application

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// AuthService autentica admins. Fase 4.1 dual-sign:
//   - Login() assina com RS256 (RSAPrivKey) e seta `kid`.
//   - ValidateAdmin() tenta RS256 primeiro; falhando, faz fallback pra
//     HS256 com LegacyHS256Secret pra aceitar tokens emitidos antes da
//     migração (janela de 7 dias). Após o cutover, basta zerar
//     LegacyHS256Secret no wire-up pra hard-disable HS256.
type AuthService struct {
	admins            domain.AdminRepository
	roles             domain.RoleRepository
	RSAPrivKey        *rsa.PrivateKey
	LegacyHS256Secret []byte
	// legacyHS256Disabled — kill-switch (Fase 4.1 follow-up). Quando true,
	// tokens HS256 são recusados mesmo com LegacyHS256Secret presente.
	// Setado via SetLegacyHS256Disabled após a janela de migração.
	legacyHS256Disabled bool
	kid                 string
	ttl                 time.Duration
	// twoFA é opcional. Sem ele, Login pula o gate de 2FA (HML/dev).
	// Setado via SetTwoFA no main wire-up quando TWOFA_ENCRYPTION_KEY presente.
	twoFA *TwoFAService
}

// SetLegacyHS256Disabled hard-disable HS256 sem reset do secret (permite
// auditoria/rotate posterior do binário).
func (s *AuthService) SetLegacyHS256Disabled(disabled bool) {
	s.legacyHS256Disabled = disabled
}

func NewAuthService(admins domain.AdminRepository, roles domain.RoleRepository, rsaKey *rsa.PrivateKey, legacyHS256Secret []byte, ttl time.Duration) *AuthService {
	return &AuthService{
		admins:            admins,
		roles:             roles,
		RSAPrivKey:        rsaKey,
		LegacyHS256Secret: legacyHS256Secret,
		kid:               deriveKID(rsaKey),
		ttl:               ttl,
	}
}

type LoginInput struct {
	Email    string
	Password string
}

type LoginResult struct {
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
	AdminID     string    `json:"admin_id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	Permissions []string  `json:"permissions"`
	// TwoFARequired sinaliza que o cliente DEVE chamar /v1/auth/login/2fa
	// passando esse PartialToken + código TOTP pra obter o token final.
	// Quando true: Token vem vazio, PartialToken vem com TTL 5min e claims
	// {typ: "admin_partial", sub: admin_id} — middleware adminAuth rejeita
	// esse typ. Quando false (admin sem requires_2fa ativo OU sem enroll):
	// Token vem direto e PartialToken vazio (back-compat com clients antigos).
	TwoFARequired       bool   `json:"twofa_required,omitempty"`
	TwoFAEnrollRequired bool   `json:"twofa_enroll_required,omitempty"`
	PartialToken        string `json:"partial_token,omitempty"`
}

func (s *AuthService) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	admin, err := s.admins.GetByEmail(ctx, email)
	if err != nil {
		return nil, domain.ErrUnauthorized
	}
	if bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(in.Password)) != nil {
		return nil, domain.ErrUnauthorized
	}
	// 2FA gate: quando requires_2fa AND TwoFA está plugado, retorna
	// partial_token + flag pro client chamar /auth/login/2fa.
	// admin.RequiresTwoFA é populado no GetByEmail (migration 036). Sem
	// TwoFA service plugado (env vazio), pula o gate — fallback p/ HML.
	if s.twoFA != nil && admin.RequiresTwoFA {
		enrolled := s.twoFA.IsEnrolled(ctx, admin.ID)
		partial, err := s.issuePartialToken(admin.ID, !enrolled)
		if err != nil {
			return nil, err
		}
		return &LoginResult{
			AdminID:             admin.ID,
			Email:               admin.Email,
			Name:                admin.Name,
			Role:                admin.Role,
			TwoFARequired:       enrolled,
			TwoFAEnrollRequired: !enrolled,
			PartialToken:        partial,
		}, nil
	}
	return s.issueFinalToken(ctx, admin)
}

// CompleteLoginWith2FA é o segundo step do login quando 2FA é requerido.
// Valida partial_token + código TOTP/backup, retorna o JWT final.
func (s *AuthService) CompleteLoginWith2FA(ctx context.Context, partialToken, code string) (*LoginResult, error) {
	if s.twoFA == nil {
		return nil, domain.ErrUnauthorized
	}
	claims, err := s.parseDualSign(partialToken)
	if err != nil {
		return nil, domain.ErrUnauthorized
	}
	if typ, _ := claims["typ"].(string); typ != "admin_partial" {
		return nil, domain.ErrUnauthorized
	}
	adminID, _ := claims["sub"].(string)
	if adminID == "" {
		return nil, domain.ErrUnauthorized
	}
	if err := s.twoFA.Verify(ctx, adminID, code); err != nil {
		return nil, domain.ErrUnauthorized
	}
	admin, err := s.admins.GetByID(ctx, adminID)
	if err != nil || admin == nil {
		return nil, domain.ErrUnauthorized
	}
	return s.issueFinalToken(ctx, admin)
}

// issueFinalToken emite o JWT admin "real" (typ=admin, TTL completo).
func (s *AuthService) issueFinalToken(ctx context.Context, admin *domain.Admin) (*LoginResult, error) {
	perms, err := s.roles.GetPermissions(ctx, admin.Role)
	if err != nil {
		return nil, err
	}
	exp := time.Now().UTC().Add(s.ttl)
	claims := jwt.MapClaims{
		"sub":   admin.ID,
		"typ":   "admin",
		"role":  admin.Role,
		"email": admin.Email,
		"exp":   exp.Unix(),
		"iat":   time.Now().UTC().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if s.kid != "" {
		tok.Header["kid"] = s.kid
	}
	signed, err := tok.SignedString(s.RSAPrivKey)
	if err != nil {
		return nil, err
	}
	return &LoginResult{
		Token:       signed,
		ExpiresAt:   exp,
		AdminID:     admin.ID,
		Email:       admin.Email,
		Name:        admin.Name,
		Role:        admin.Role,
		Permissions: perms,
	}, nil
}

// issuePartialToken emite JWT curto (5min) com typ=admin_partial. middleware
// adminAuth rejeita esse typ — só pode ser usado em /auth/login/2fa.
// `enrollNeeded` é embutido pro front saber qual UI mostrar (setup wizard vs
// código direto).
func (s *AuthService) issuePartialToken(adminID string, enrollNeeded bool) (string, error) {
	exp := time.Now().UTC().Add(5 * time.Minute)
	claims := jwt.MapClaims{
		"sub":           adminID,
		"typ":           "admin_partial",
		"enroll_needed": enrollNeeded,
		"exp":           exp.Unix(),
		"iat":           time.Now().UTC().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if s.kid != "" {
		tok.Header["kid"] = s.kid
	}
	return tok.SignedString(s.RSAPrivKey)
}

// SetTwoFA pluga o serviço de 2FA. Sem isso, Login pula o gate (HML/dev).
func (s *AuthService) SetTwoFA(t *TwoFAService) { s.twoFA = t }

// ParsePartialToken valida um partial_token (typ=admin_partial) e retorna
// o adminID. Usado pelo handler de enroll pra autorizar a chamada sem
// exigir JWT admin completo (admin ainda não terminou 2FA setup).
func (s *AuthService) ParsePartialToken(token string) (string, error) {
	claims, err := s.parseDualSign(token)
	if err != nil {
		return "", domain.ErrUnauthorized
	}
	if typ, _ := claims["typ"].(string); typ != "admin_partial" {
		return "", domain.ErrUnauthorized
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", domain.ErrUnauthorized
	}
	return sub, nil
}

// ValidateAdmin valida o token de admin e monta o Principal (com permissões
// carregadas do papel — sempre frescas, não confia em perms embutidas no JWT).
//
// Dual-sign: tenta RS256 primeiro (token novo). Se o header indicar HS256
// e LegacyHS256Secret estiver configurado, aceita como legado.
func (s *AuthService) ValidateAdmin(ctx context.Context, tokenStr string) (domain.Principal, error) {
	claims, err := s.parseDualSign(tokenStr)
	if err != nil {
		return domain.Principal{}, err
	}
	if typ, _ := claims["typ"].(string); typ != "admin" {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	sub, _ := claims["sub"].(string)
	role, _ := claims["role"].(string)
	if sub == "" || role == "" {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	perms, err := s.roles.GetPermissions(ctx, role)
	if err != nil {
		return domain.Principal{}, domain.ErrUnauthorized
	}
	return domain.Principal{AdminID: sub, Role: role, Permissions: perms}, nil
}

// parseDualSign aceita tanto RS256 (atual) quanto HS256 (legado) durante
// a janela de transição. O keyfunc inspeciona t.Method e devolve a chave
// apropriada; falhas implícitas (alg "none", chave faltando, etc.)
// caem em ErrUnauthorized.
func (s *AuthService) parseDualSign(tokenStr string) (jwt.MapClaims, error) {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		switch t.Method.(type) {
		case *jwt.SigningMethodRSA:
			if s.RSAPrivKey == nil {
				return nil, domain.ErrUnauthorized
			}
			return &s.RSAPrivKey.PublicKey, nil
		case *jwt.SigningMethodHMAC:
			if s.legacyHS256Disabled || len(s.LegacyHS256Secret) == 0 {
				return nil, domain.ErrUnauthorized
			}
			return s.LegacyHS256Secret, nil
		default:
			return nil, domain.ErrUnauthorized
		}
	})
	if err != nil || !t.Valid {
		return nil, domain.ErrUnauthorized
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return nil, domain.ErrUnauthorized
	}
	return claims, nil
}

// CurrentPrincipal recarrega o principal a partir do papel (uso em /admin/me).
func (s *AuthService) Roles(ctx context.Context) ([]domain.Role, error) {
	return s.roles.List(ctx)
}

// GetAdminByID — usado pelo handler AdminBecomeCustomer pra ler email/name
// do principal autenticado.
func (s *AuthService) GetAdminByID(ctx context.Context, id string) (*domain.Admin, error) {
	return s.admins.GetByID(ctx, id)
}

// AdminListAdmins devolve todos os admins p/ a UI de gestão (RBAC). Inclui
// password_hash (que o handler NÃO deve emitir) — caller faz o stripping.
func (s *AuthService) AdminListAdmins(ctx context.Context) ([]domain.Admin, error) {
	return s.admins.ListAll(ctx)
}

// AdminCreate cria um novo admin com role escolhido. Senha é gerada e
// devolvida UMA vez ao caller (admin promotor anota e envia ao novo admin).
// 2FA fica obrigatório por default (requires_2fa = true).
func (s *AuthService) AdminCreate(ctx context.Context, email, name, role string) (*domain.Admin, string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	name = strings.TrimSpace(name)
	role = strings.TrimSpace(role)
	if email == "" || name == "" || role == "" {
		return nil, "", domain.ErrInvalidInput
	}
	if !validAdminRole(role) {
		return nil, "", domain.ErrInvalidInput
	}
	if existing, _ := s.admins.GetByEmail(ctx, email); existing != nil {
		return nil, "", domain.ErrConflict
	}
	pwd := GeneratePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(pwd), 12)
	if err != nil {
		return nil, "", err
	}
	a := domain.Admin{
		ID:            uuid.New().String(),
		Email:         email,
		Name:          name,
		Role:          role,
		PasswordHash:  string(hash),
		RequiresTwoFA: true,
	}
	if err := s.admins.Create(ctx, a); err != nil {
		return nil, "", err
	}
	return &a, pwd, nil
}

// AdminUpdateRole troca o role de um admin. Bloqueia troca em quem é
// superadmin se o caller não for superadmin (garante que admin com
// permissão admins:manage mas sem role superadmin não consegue rebaixar
// um superadmin nem se auto-promover).
func (s *AuthService) AdminUpdateRole(ctx context.Context, callerPrincipal domain.Principal, targetID, newRole string) error {
	newRole = strings.TrimSpace(newRole)
	if !validAdminRole(newRole) {
		return domain.ErrInvalidInput
	}
	target, err := s.admins.GetByID(ctx, targetID)
	if err != nil {
		return err
	}
	// Só superadmin pode mexer em outro superadmin ou criar um novo.
	if (target.Role == domain.RoleSuperadmin || newRole == domain.RoleSuperadmin) &&
		callerPrincipal.Role != domain.RoleSuperadmin {
		return domain.ErrForbidden
	}
	// Não permite admin atualizar o próprio role (vetor de auto-promoção).
	if callerPrincipal.AdminID == targetID && callerPrincipal.Role != domain.RoleSuperadmin {
		return domain.ErrForbidden
	}
	return s.admins.UpdateRole(ctx, targetID, newRole)
}

// AdminDelete remove um admin. Mesma proteção do UpdateRole: só superadmin
// remove outro superadmin; admin não consegue se auto-deletar.
func (s *AuthService) AdminDelete(ctx context.Context, callerPrincipal domain.Principal, targetID string) error {
	if callerPrincipal.AdminID == targetID {
		return domain.ErrForbidden
	}
	target, err := s.admins.GetByID(ctx, targetID)
	if err != nil {
		return err
	}
	if target.Role == domain.RoleSuperadmin && callerPrincipal.Role != domain.RoleSuperadmin {
		return domain.ErrForbidden
	}
	return s.admins.Delete(ctx, targetID)
}

// validAdminRole rejeita strings que não batem com nenhuma role na tabela
// roles. Caller deve garantir consistência (PK em roles.code + FK opcional).
// Lista hard-coded espelha os 4 seedados em migration 026; novas roles
// precisam ser adicionadas aqui também (defesa em profundidade).
func validAdminRole(role string) bool {
	switch role {
	case domain.RoleSuperadmin, "manager", "support", "viewer":
		return true
	}
	return false
}

func deriveKID(priv *rsa.PrivateKey) string {
	if priv == nil {
		return ""
	}
	sum := sha256.Sum256(priv.PublicKey.N.Bytes())
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}
