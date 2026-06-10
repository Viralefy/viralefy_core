package application

import (
	"context"
	"crypto/rsa"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// RevocationChecker — interface mínima implementada por
// infrastructure/auth.RevocationCache. Usada como defense-in-depth (camada 2)
// em ValidateToken: se o jti do token está revogado, rejeita mesmo que
// signature/exp estejam válidos. O dispatcher (viralefy_api_rust) é a camada
// 1 — esta cache cobre o caso de bypass do dispatcher (loopback, mesh
// interno, debugging direto na porta 8084).
//
// Opt-in: se a cache não foi setada (cache == nil em ValidateToken),
// a checagem é pulada — preserva back-compat com testes e binários sem
// DB.
type RevocationChecker interface {
	IsRevoked(jti string) bool
}

// UserAuthService — mesma estratégia dual-sign do AuthService (Fase 4.1).
type UserAuthService struct {
	users             domain.UserRepository
	RSAPrivKey        *rsa.PrivateKey
	LegacyHS256Secret []byte
	// legacyHS256Disabled — kill-switch (Fase 4.1 follow-up). Espelha o
	// comportamento do AuthService de admin: bloqueia HS256 mesmo com
	// secret presente, pra cutover seguro sem reiniciar com env zerada.
	legacyHS256Disabled bool
	kid                 string
	ttl                 time.Duration
	// referrals opcional. Quando setado, Register chama RecordReferral se
	// tracking[referrer_code] estiver presente. Best-effort.
	referrals *ReferralService
	// twoFA opcional. Quando o user tem 2FA enrolled, Login retorna
	// UserSession com partial token; Complete2FA finaliza. User 2FA é
	// OPCIONAL — se não enrolled, login passa direto (diferente de admin).
	twoFA *TwoFAService
	// revocationCache opcional — defense-in-depth de jtis revogados.
	// Setado via SetRevocationCache no main wire-up quando DATABASE_URL
	// está disponível. Nil-safe: nil = pula a checagem (dispatcher cobre).
	revocationCache RevocationChecker
}

// SetRevocationCache pluga a cache de jtis revogados. Defense-in-depth:
// se nil, ValidateToken pula a checagem (dispatcher é a camada primária).
func (s *UserAuthService) SetRevocationCache(rc RevocationChecker) {
	s.revocationCache = rc
}

// SetTwoFA pluga o serviço de 2FA pra usuários.
func (s *UserAuthService) SetTwoFA(t *TwoFAService) { s.twoFA = t }

// normalizeTelegram canonicaliza a entrada do usuário. Aceita:
//   "@handle"             → "@handle"
//   "handle"              → "@handle"
//   "t.me/handle"         → "@handle"
//   "https://t.me/handle" → "@handle"
//
// Mantém prefix "@" pra ficar óbvio no painel admin que é handle, não phone.
func normalizeTelegram(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimPrefix(raw, "t.me/")
	raw = strings.TrimPrefix(raw, "telegram.me/")
	if !strings.HasPrefix(raw, "@") {
		raw = "@" + raw
	}
	return raw
}

// IsTwoFAEnrolled — consultado pelo front em /v1/me/2fa/status.
func (s *UserAuthService) IsTwoFAEnrolled(ctx context.Context, userID string) bool {
	if s.twoFA == nil {
		return false
	}
	return s.twoFA.IsEnrolled(ctx, userID)
}

// TwoFA expõe o service pra handlers (enroll/verify/disable).
func (s *UserAuthService) TwoFA() *TwoFAService { return s.twoFA }

// SetReferrals opt-in.
func (s *UserAuthService) SetReferrals(svc *ReferralService) {
	s.referrals = svc
}

// SetLegacyHS256Disabled — ver AuthService.SetLegacyHS256Disabled.
func (s *UserAuthService) SetLegacyHS256Disabled(disabled bool) {
	s.legacyHS256Disabled = disabled
}

func NewUserAuthService(users domain.UserRepository, rsaKey *rsa.PrivateKey, legacyHS256Secret []byte, ttl time.Duration) *UserAuthService {
	return &UserAuthService{
		users:             users,
		RSAPrivKey:        rsaKey,
		LegacyHS256Secret: legacyHS256Secret,
		kid:               deriveKID(rsaKey),
		ttl:               ttl,
	}
}

type RegisterInput struct {
	Email    string
	Name     string
	Password string
	// Phone + Telegram — canal alternativo de contato. Pelo menos UM é
	// obrigatório (validado em Register). Phone aceita formato livre;
	// Telegram aceita @handle ou link t.me/.
	Phone    string
	Telegram string
	// Tracking first-touch (utm/fbclid/referrer/landing_url enriquecido com
	// IP+UA server-side). Persistido em users.tracking_data.
	Tracking map[string]any
}

type UserSession struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      UserView  `json:"user"`
	// TwoFARequired sinaliza que UserSession é só estágio 1 (partial).
	// Cliente DEVE chamar POST /v1/auth/user/login/2fa com partial_token +
	// código pra obter Token final. User 2FA é opcional — só vem quando o
	// usuário fez enroll prévio.
	TwoFARequired bool   `json:"twofa_required,omitempty"`
	PartialToken  string `json:"partial_token,omitempty"`
}

type UserView struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Instagram string `json:"instagram"`
	Phone     string `json:"phone,omitempty"`
	Telegram  string `json:"telegram,omitempty"`
}

func (s *UserAuthService) Register(ctx context.Context, in RegisterInput) (*UserSession, error) {
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	in.Phone = strings.TrimSpace(in.Phone)
	in.Telegram = strings.TrimSpace(in.Telegram)
	if in.Email == "" || in.Name == "" || len(in.Password) < 8 {
		return nil, domain.ErrInvalidInput
	}
	// Pelo menos um canal alternativo. Phone OU Telegram — slash no nome
	// do campo indica "qualquer um dos dois". Suporte usa esse canal pra
	// contornar email em spam, reduzir refund por "nunca fui notificado".
	if in.Phone == "" && in.Telegram == "" {
		return nil, domain.ErrInvalidInput
	}
	if existing, _ := s.users.GetByEmail(ctx, in.Email); existing != nil {
		return nil, domain.ErrConflict
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if err != nil {
		return nil, err
	}
	u := domain.User{
		ID:           uuid.New().String(),
		Email:        in.Email,
		Name:         in.Name,
		Instagram:    "", // legado — perfis ficam em /v1/me/profiles agora
		Phone:        in.Phone,
		Telegram:     normalizeTelegram(in.Telegram),
		PasswordHash: string(hash),
		TrackingData: in.Tracking,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	// Referral signup hook — espelha CheckoutService.
	if s.referrals != nil {
		if rc, ok := in.Tracking["referrer_code"].(string); ok && rc != "" {
			_ = s.referrals.RecordReferral(ctx, u.ID, rc)
		}
	}
	return s.session(u)
}

func (s *UserAuthService) Login(ctx context.Context, email, password string) (*UserSession, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	u, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		return nil, domain.ErrUnauthorized
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, domain.ErrUnauthorized
	}
	// 2FA gate (OPCIONAL pro user): bloqueia em partial_token SÓ se
	// user já fez enroll. Sem 2FA → login direto (diferente de admin
	// que tem requires_2fa default TRUE).
	if s.twoFA != nil && s.twoFA.IsEnrolled(ctx, u.ID) {
		partial, err := s.issuePartialUserToken(u.ID)
		if err != nil {
			return nil, err
		}
		return &UserSession{
			User:          UserView{ID: u.ID, Email: u.Email, Name: u.Name, Instagram: u.Instagram, Phone: u.Phone, Telegram: u.Telegram},
			TwoFARequired: true,
			PartialToken:  partial,
		}, nil
	}
	return s.session(*u)
}

// CompleteLoginWith2FA — segundo step quando user tem 2FA. partial_token
// tem typ=user_partial; valida + verifica código + retorna sessão final.
func (s *UserAuthService) CompleteLoginWith2FA(ctx context.Context, partialToken, code string) (*UserSession, error) {
	if s.twoFA == nil {
		return nil, domain.ErrUnauthorized
	}
	claims, err := s.parseDualSign(partialToken)
	if err != nil {
		return nil, domain.ErrUnauthorized
	}
	if typ, _ := claims["typ"].(string); typ != "user_partial" {
		return nil, domain.ErrUnauthorized
	}
	userID, _ := claims["sub"].(string)
	if userID == "" {
		return nil, domain.ErrUnauthorized
	}
	if err := s.twoFA.Verify(ctx, userID, code); err != nil {
		return nil, domain.ErrUnauthorized
	}
	u, err := s.users.GetByID(ctx, userID)
	if err != nil || u == nil {
		return nil, domain.ErrUnauthorized
	}
	return s.session(*u)
}

func (s *UserAuthService) issuePartialUserToken(userID string) (string, error) {
	exp := time.Now().UTC().Add(5 * time.Minute)
	claims := jwt.MapClaims{
		"sub": userID,
		"typ": "user_partial",
		"exp": exp.Unix(),
		"iat": time.Now().UTC().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if s.kid != "" {
		tok.Header["kid"] = s.kid
	}
	return tok.SignedString(s.RSAPrivKey)
}

// EnsureShadowAccount cria (se não existir) um user com o email/name do
// admin e devolve uma UserSession. Usado pelo botão "Open customer side"
// no backoffice — admin testa o fluxo de compra como customer sem precisar
// de outra conta.
//
// Política de senha: se o user é criado agora, gera senha aleatória forte
// e DEVOLVE em GeneratedPassword na response (mostrar UMA vez no backoffice
// e o admin guarda em password manager se quiser usar /login depois).
// Se user já existe, GeneratedPassword fica vazio.
func (s *UserAuthService) EnsureShadowAccount(ctx context.Context, email, name string) (*UserSession, string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil, "", domain.ErrInvalidInput
	}
	if existing, _ := s.users.GetByEmail(ctx, email); existing != nil {
		sess, err := s.session(*existing)
		return sess, "", err
	}
	if name == "" {
		name = email
	}
	pwd := GeneratePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(pwd), 12)
	if err != nil {
		return nil, "", err
	}
	u := domain.User{
		ID:           uuid.New().String(),
		Email:        email,
		Name:         name,
		PasswordHash: string(hash),
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, "", err
	}
	sess, err := s.session(u)
	if err != nil {
		return nil, "", err
	}
	return sess, pwd, nil
}

func (s *UserAuthService) session(u domain.User) (*UserSession, error) {
	exp := time.Now().UTC().Add(s.ttl)
	claims := jwt.MapClaims{
		"sub":  u.ID,
		"role": "user",
		"exp":  exp.Unix(),
		"iat":  time.Now().UTC().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if s.kid != "" {
		tok.Header["kid"] = s.kid
	}
	signed, err := tok.SignedString(s.RSAPrivKey)
	if err != nil {
		return nil, err
	}
	return &UserSession{
		Token:     signed,
		ExpiresAt: exp,
		User:      UserView{ID: u.ID, Email: u.Email, Name: u.Name, Instagram: u.Instagram, Phone: u.Phone, Telegram: u.Telegram},
	}, nil
}

// parseDualSign aceita RS256 (atual) ou HS256 (legacy). Usado pro
// partial token de 2FA (typ=user_partial) que não passa pelo ValidateToken
// padrão (esse exige role=user no claim).
func (s *UserAuthService) parseDualSign(tokenStr string) (jwt.MapClaims, error) {
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

func (s *UserAuthService) ValidateToken(tokenStr string) (userID string, err error) {
	t, perr := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
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
	if perr != nil || !t.Valid {
		return "", domain.ErrUnauthorized
	}
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return "", domain.ErrUnauthorized
	}
	if role, _ := claims["role"].(string); role != "user" {
		return "", domain.ErrUnauthorized
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", domain.ErrUnauthorized
	}
	// Defense-in-depth (camada 2): jti revogado bloqueia mesmo com
	// signature/exp válidos. O dispatcher já rejeita antes (camada 1) —
	// este check só dispara em bypass (loopback/mesh interno).
	// Nil-safe: sem cache plugada, pula a checagem.
	if s.revocationCache != nil {
		if jti, _ := claims["jti"].(string); jti != "" && s.revocationCache.IsRevoked(jti) {
			return "", domain.ErrUnauthorized
		}
	}
	return sub, nil
}
