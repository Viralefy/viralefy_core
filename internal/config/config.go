package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port        string
	BindHost    string
	DatabaseURL string
	JWTSecret   string
	JWTTTL      time.Duration
	// JWTPrivateKeyPath é o caminho da chave RSA privada usada pra
	// assinar tokens RS256 (Fase 4.1). Se o arquivo não existir, é
	// gerado on-demand pelo jwtkeys.LoadOrGenerate.
	JWTPrivateKeyPath string
	// LegacyHS256Disabled — quando true, o validador para de aceitar
	// tokens HS256 legados. Setar após a janela de 7 dias.
	LegacyHS256Disabled bool
	CORSOrigins         []string
	SMTPAddr            string
	SMTPUser            string
	SMTPPass            string
	SMTPFrom            string
	SMTPFromName        string

	EmailProvider       string
	ResendAPIKey        string
	ResendFrom          string
	ResendFromName      string
	ResendBaseURL       string
	ResendWebhookSecret string

	SiteURL string // URL pública da loja (https://viralefy.com) — usada em e-mails

	// Cloudflare Turnstile (anti-bot). Secret vazia = bypass (HML/dev).
	TurnstileSecretKey string

	// Webhook genérico (Slack/Discord-compatible) pra notificar admin
	// quando um ticket de high-touch (recovery/BM/perfil) abre. Vazio = no-op.
	AdminWebhookURL string

	// Object storage (MinIO local / Cloudflare R2). S3-compat. Endpoint vazio
	// = storage disabled (handler retorna ErrNotImplemented, sistema cai no
	// fluxo legado de base64 inline pra back-compat durante a migração).
	Storage StorageConfig

	// TwoFAEncryptionKey — AES-256 (32 bytes) pra cifrar secrets TOTP em
	// rest. Hex 64 chars OU base64 44 chars. Vazio = 2FA disabled (handlers
	// retornam 503). Instalador gera + persiste em /etc/viralefy/.env.
	TwoFAEncryptionKey []byte

	// Microservices (PHASE-8). Loopback-only; o monolito é o único
	// cliente. Vazio = wiring fica em modo legado (in-memory providers /
	// SMTP/Resend direto), sem trocar o registry — Wave 3 ainda não rodou.
	//
	//   PaymentsInternalURL  — base URL do viralefy_payments
	//                          (ex.: http://127.0.0.1:8081). Sem trailing /.
	//   SenderInternalURL    — base URL do viralefy_sender
	//                          (ex.: http://127.0.0.1:8082).
	//   InternalSharedSecret — token em X-Internal-Token entre services.
	//                          Gerado pelo installer e propagado pros 3
	//                          systemd units via /etc/viralefy/.env.
	PaymentsInternalURL  string
	SenderInternalURL    string
	InternalSharedSecret string

	// TelegramAdminChatID — chat_id (numérico em string) ou @canalhandle do
	// canal interno do admin. Usado pelo PaymentReceiver pra disparar
	// notificação via viralefy_sender (channel="telegram") em cada paid
	// order. Vazio = não dispara (HML/POC sem bot configurado ainda).
	TelegramAdminChatID string
}

type StorageConfig struct {
	Endpoint        string // 127.0.0.1:9000 ou <acct>.r2.cloudflarestorage.com
	AccessKey       string
	SecretKey       string
	Region          string
	UseSSL          bool
	BucketProofs    string
	BucketPublic    string
}

func (s StorageConfig) Enabled() bool { return s.Endpoint != "" && s.AccessKey != "" }

func Load() (Config, error) {
	port := getenv("PORT", "8080")
	// Default seguro: só localhost. Production fica atrás do Caddy. Para expor
	// externamente sem proxy, defina BIND_HOST=0.0.0.0 explicitamente.
	bindHost := getenv("BIND_HOST", "127.0.0.1")
	db := getenv("DATABASE_URL", "postgres://viralefy:viralefy@localhost:5432/viralefy?sslmode=disable")
	secret := getenv("JWT_SECRET", "change-me-in-production-min-32-chars!!")
	ttlHours, _ := strconv.Atoi(getenv("JWT_TTL_HOURS", "24"))
	cors := getenv("CORS_ORIGINS", "http://localhost:3000,http://localhost:3001")
	cfg := Config{
		Port:                port,
		BindHost:            bindHost,
		DatabaseURL:         db,
		JWTSecret:           secret,
		JWTTTL:              time.Duration(ttlHours) * time.Hour,
		JWTPrivateKeyPath:   getenv("JWT_PRIVATE_KEY_PATH", "/etc/viralefy/jwt-rs256.pem"),
		LegacyHS256Disabled: getenv("LEGACY_HS256_DISABLED", "") == "true",
		CORSOrigins:         splitCSV(cors),
		SMTPAddr:            getenv("SMTP_ADDR", ""),
		SMTPUser:            getenv("SMTP_USER", ""),
		SMTPPass:            getenv("SMTP_PASS", ""),
		SMTPFrom:            getenv("SMTP_FROM", "no-reply@viralefy.local"),
		SMTPFromName:        getenv("SMTP_FROM_NAME", "Viralefy"),

		EmailProvider:       getenv("EMAIL_PROVIDER", ""),
		ResendAPIKey:        getenv("RESEND_API_KEY", ""),
		ResendFrom:          getenv("RESEND_FROM", "onboarding@resend.dev"),
		ResendFromName:      getenv("RESEND_FROM_NAME", "Viralefy"),
		ResendBaseURL:       getenv("RESEND_BASE_URL", "https://api.resend.com"),
		ResendWebhookSecret: getenv("RESEND_WEBHOOK_SECRET", ""),

		SiteURL: getenv("SITE_URL", getenv("NEXT_PUBLIC_SITE_URL", "https://viralefy.com")),

		TurnstileSecretKey: getenv("TURNSTILE_SECRET_KEY", ""),
		AdminWebhookURL:    getenv("ADMIN_WEBHOOK_URL", ""),

		PaymentsInternalURL:  strings.TrimRight(getenv("PAYMENTS_INTERNAL_URL", ""), "/"),
		SenderInternalURL:    strings.TrimRight(getenv("SENDER_INTERNAL_URL", ""), "/"),
		InternalSharedSecret: getenv("INTERNAL_SHARED_SECRET", ""),
		TelegramAdminChatID:  strings.TrimSpace(getenv("TELEGRAM_ADMIN_CHAT_ID", "")),

		Storage: StorageConfig{
			Endpoint:     strings.TrimPrefix(strings.TrimPrefix(getenv("STORAGE_ENDPOINT", ""), "https://"), "http://"),
			AccessKey:    getenv("STORAGE_ACCESS_KEY", ""),
			SecretKey:    getenv("STORAGE_SECRET_KEY", ""),
			Region:       getenv("STORAGE_REGION", "us-east-1"),
			UseSSL:       getenv("STORAGE_USE_SSL", "false") == "true",
			BucketProofs: getenv("STORAGE_BUCKET_PROOFS", "viralefy-proofs"),
			BucketPublic: getenv("STORAGE_BUCKET_PUBLIC", "viralefy-public"),
		},
	}
	// 2FA encryption key: hex(64) ou base64(44). Vazio → 2FA off.
	if raw := strings.TrimSpace(getenv("TWOFA_ENCRYPTION_KEY", "")); raw != "" {
		if b, err := parse2FAKey(raw); err == nil {
			cfg.TwoFAEncryptionKey = b
		}
	}
	if len(cfg.JWTSecret) < 16 {
		return cfg, fmt.Errorf("JWT_SECRET must be at least 16 characters")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range split(s, ',') {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parse2FAKey aceita hex 64 chars OU base64 44 (com padding) / 43 (sem).
// Retorna []byte len=32 ou erro. Inline aqui pra não criar dep cycle
// config → application/totp.
func parse2FAKey(s string) ([]byte, error) {
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("TWOFA_ENCRYPTION_KEY must be 32 bytes (hex 64 or base64 44/43 chars)")
}

func split(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			parts = append(parts, trim(s[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, trim(s[start:]))
	return parts
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
