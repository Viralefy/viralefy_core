package application

import (
	"context"
	"log"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

// WhatsApp transactional sender (Fase 7.3).
//
// Por enquanto em HML/POC o provider real (Meta Cloud API / Twilio) ainda
// não está plugado; o sender padrão é DryRun: loga e devolve sucesso. Isso
// permite que callers (order updates, OTP, etc.) já chamem
// NotifyIfOptedIn sem branch condicional — o switch pra um provider real
// é trocar a implementação do WhatsAppSender no wire em main.go.
//
// Política de opt-in:
//   * Default whatsapp_opt_in=false (migration 028). Nada é enviado até o
//     usuário ligar o toggle em /account/notifications.
//   * Numero precisa ter prefixo "+" e ao menos 8 dígitos: validação fraca
//     proposital — provider real fará a validação dura. Aqui só evita
//     injeção/lixo óbvio antes de persistir.
//   * NotifyIfOptedIn é fire-only-when-allowed: silently no-op se o
//     usuário não optou ou não tem número cadastrado. Caller não precisa
//     conhecer o estado do usuário.

// WhatsAppMessage é a mensagem mínima que o sender consome.
type WhatsAppMessage struct {
	To   string
	Body string
}

// WhatsAppSender é a porta de saída. A implementação concreta (Meta /
// Twilio) viverá em infrastructure/external/whatsapp quando integrarmos.
type WhatsAppSender interface {
	Send(ctx context.Context, msg WhatsAppMessage) error
}

// DryRunWhatsAppSender — implementação default pra HML/POC. Loga e
// devolve nil. Não toca em rede, então é seguro mesmo em testes.
type DryRunWhatsAppSender struct{}

func NewDryRunWhatsAppSender() *DryRunWhatsAppSender {
	return &DryRunWhatsAppSender{}
}

func (s *DryRunWhatsAppSender) Send(_ context.Context, msg WhatsAppMessage) error {
	log.Printf("[whatsapp:dryrun] to=%s body=%q", msg.To, msg.Body)
	return nil
}

// WhatsAppService — gerencia opt-in/número do usuário e dispara mensagens
// transacionais. Wrapper fino: estado é per-user na tabela users (sem
// repo dedicado), e o envio é delegado ao sender.
type WhatsAppService struct {
	db     *postgres.DB
	sender WhatsAppSender
}

func NewWhatsAppService(db *postgres.DB, sender WhatsAppSender) *WhatsAppService {
	return &WhatsAppService{db: db, sender: sender}
}

// WhatsAppPref é o snapshot devolvido ao handler GET.
type WhatsAppPref struct {
	Number string `json:"number"`
	OptIn  bool   `json:"opt_in"`
}

// GetPref devolve número + opt_in do usuário. Sem dependência de notif_prefs
// (essas 4 chaves são e-mail). WhatsApp tem flag dedicada porque carrega
// PII (número) que precisa de coluna nullable e índice futuro.
func (s *WhatsAppService) GetPref(ctx context.Context, userID string) (WhatsAppPref, error) {
	if s == nil || s.db == nil {
		return WhatsAppPref{}, domain.ErrInvalidInput
	}
	if userID == "" {
		return WhatsAppPref{}, domain.ErrUnauthorized
	}
	var number *string
	var optIn bool
	err := s.db.Pool().QueryRow(ctx,
		`SELECT whatsapp_number, whatsapp_opt_in FROM users WHERE id=$1`,
		userID,
	).Scan(&number, &optIn)
	if err != nil {
		return WhatsAppPref{}, err
	}
	out := WhatsAppPref{OptIn: optIn}
	if number != nil {
		out.Number = *number
	}
	return out, nil
}

// UpdateNumber normaliza pro formato E.164 (basicamente: prefixo "+",
// resto dígitos). Não consultamos provider — validação real fica pro
// envio. Aqui só sanity check antes de gravar no banco.
//
// Caso especial: número vazio = limpar (usuário removeu o número).
// Mantemos opt_in como está; caller pode chamar OptIn(false) se quiser.
func (s *WhatsAppService) UpdateNumber(ctx context.Context, userID, number string) error {
	if s == nil || s.db == nil {
		return domain.ErrInvalidInput
	}
	if userID == "" {
		return domain.ErrUnauthorized
	}
	number = strings.TrimSpace(number)
	if number == "" {
		_, err := s.db.Pool().Exec(ctx,
			`UPDATE users SET whatsapp_number=NULL WHERE id=$1`, userID)
		return err
	}
	normalized, ok := normalizeE164(number)
	if !ok {
		return domain.ErrInvalidInput
	}
	_, err := s.db.Pool().Exec(ctx,
		`UPDATE users SET whatsapp_number=$2 WHERE id=$1`, userID, normalized)
	return err
}

// OptIn liga/desliga a flag de opt-in. Não força ter número: se o usuário
// liga sem número, NotifyIfOptedIn vai silenciar até ele cadastrar.
func (s *WhatsAppService) OptIn(ctx context.Context, userID string, optIn bool) error {
	if s == nil || s.db == nil {
		return domain.ErrInvalidInput
	}
	if userID == "" {
		return domain.ErrUnauthorized
	}
	_, err := s.db.Pool().Exec(ctx,
		`UPDATE users SET whatsapp_opt_in=$2 WHERE id=$1`, userID, optIn)
	return err
}

// NotifyIfOptedIn dispara mensagem se (a) o usuário existe, (b) opt_in=true
// e (c) tem número cadastrado. Qualquer outra coisa → no-op silencioso.
// Caller (order_service, etc.) pode chamar sem checar nada antes.
//
// Erros do sender são propagados pra o caller decidir se reenvia. Falha
// no lookup também propaga (DB indisponível é problema do caller saber).
func (s *WhatsAppService) NotifyIfOptedIn(ctx context.Context, userID, body string) error {
	if s == nil || s.db == nil || s.sender == nil {
		return nil
	}
	if userID == "" || body == "" {
		return nil
	}
	var number *string
	var optIn bool
	err := s.db.Pool().QueryRow(ctx,
		`SELECT whatsapp_number, whatsapp_opt_in FROM users WHERE id=$1`,
		userID,
	).Scan(&number, &optIn)
	if err != nil {
		return err
	}
	if !optIn || number == nil || *number == "" {
		return nil
	}
	return s.sender.Send(ctx, WhatsAppMessage{To: *number, Body: body})
}

// normalizeE164 é uma validação intencionalmente fraca: aceita "+" seguido
// de 8-15 dígitos (E.164 max é 15). Espaços, traços e parênteses são
// strippados antes da checagem porque o usuário cola do contato da agenda.
// Provider real (Meta/Twilio) vai validar de verdade no envio.
func normalizeE164(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "+") {
		return "", false
	}
	rest := raw[1:]
	var digits strings.Builder
	for _, r := range rest {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
			// strip
		default:
			return "", false
		}
	}
	d := digits.String()
	if len(d) < 8 || len(d) > 15 {
		return "", false
	}
	return "+" + d, true
}
