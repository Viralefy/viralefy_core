package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// CategoriesOpeningTicket lista as categorias cujo pedido pago abre um
// ticket de suporte automaticamente. Espelho de TICKET_OPENING_CATEGORIES
// no front. Mudanças aqui devem espelhar lá.
//
// O ticket é aberto com prioridade "normal" (default do TicketService.Open);
// admin pode subir pra "high" via backoffice quando triagem confirmar
// urgência.
var CategoriesOpeningTicket = map[string]bool{
	"recuperacao_perfil": true,
	"bms_facebook":       true,
	"perfis_redes":       true,
}

// AdminNotifier é a porta de saída para webhook administrativo (Slack/
// Discord). Implementação concreta em infrastructure/external/notify.
type AdminNotifier interface {
	Send(ctx context.Context, text string) error
	Enabled() bool
}

// TelegramNotifier é a porta de saída pra notificações via Telegram bot.
// Implementação concreta em infrastructure/external/senderclient (chama
// viralefy_sender que renderiza template + dispara via Bot API). Vazio
// no modo legacy (sem microservice de sender) — PaymentReceiver no-op.
//
// Handle é "@username" (resolvido pelo sender via telegram_chats) ou
// chat_id numérico em string. Template é o nome registrado no sender
// (ex.: "checkout_paid_admin", "checkout_paid_customer"). Vars alimenta
// a substituição do template.
type TelegramNotifier interface {
	SendTelegram(ctx context.Context, handle, template string, vars map[string]string) error
}

// PaymentReceiver é o ponto único de entrada para confirmações de pagamento
// (webhook ou ação manual do admin). Idempotente: chamadas repetidas para
// o mesmo external_ref / id já pago são no-op.
type PaymentReceiver struct {
	invoices   domain.InvoiceRepository
	orders     domain.OrderRepository
	plans      domain.PlanRepository
	users      domain.UserRepository
	tickets    *TicketService
	invoiceSvc *InvoiceService
	email      EmailSender
	notifier   AdminNotifier
	siteURL    string
	referrals  *ReferralService
	// telegram + adminTelegramChat — opt-in via SetTelegram (PHASE-8 Wave 3).
	// Vazio = sem notificação Telegram (modo legado / HML sem bot ainda).
	telegram         TelegramNotifier
	adminTelegramTo  string
}

// SetReferrals opt-in pra payout hook (GrantOnFirstPaidOrder após order
// vira paid). Best-effort: erro no payout não impede transição de status.
func (r *PaymentReceiver) SetReferrals(svc *ReferralService) {
	r.referrals = svc
}

// SetTelegram opt-in pra notificação via Telegram bot quando um pedido
// vira paid (Wave 3). adminChat é o chat_id/handle do canal interno;
// pode vir vazio (= não notifica admin, mas ainda dispara pro cliente se
// ele tiver telegram cadastrado). Notifier nil = no-op completo.
func (r *PaymentReceiver) SetTelegram(notifier TelegramNotifier, adminChat string) {
	r.telegram = notifier
	r.adminTelegramTo = strings.TrimSpace(adminChat)
}

func NewPaymentReceiver(
	invoices domain.InvoiceRepository,
	orders domain.OrderRepository,
	plans domain.PlanRepository,
	users domain.UserRepository,
	tickets *TicketService,
	invoiceSvc *InvoiceService,
	email EmailSender,
	notifier AdminNotifier,
	siteURL string,
) *PaymentReceiver {
	return &PaymentReceiver{
		invoices:   invoices,
		orders:     orders,
		plans:      plans,
		users:      users,
		tickets:    tickets,
		invoiceSvc: invoiceSvc,
		email:      email,
		notifier:   notifier,
		siteURL:    siteURL,
	}
}

// ConfirmByExternalRef tenta achar invoice OU order com aquele external_ref
// e marca como paga. Retorna o "tipo" identificado ("invoice", "order" ou
// vazio) para o caller logar. Erros de lookup são ignorados (provider pode
// mandar webhook para algo que já foi processado / não existe).
func (r *PaymentReceiver) ConfirmByExternalRef(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("external_ref vazio")
	}
	if inv, err := r.invoices.GetByExternalRef(ctx, ref); err == nil && inv != nil {
		if inv.Status == domain.InvoiceStatusPaid {
			return "invoice", nil
		}
		if _, err := r.invoiceSvc.AdminMarkPaid(ctx, inv.ID); err != nil {
			return "invoice", err
		}
		observability.FromContext(ctx).Info("invoice confirmed",
			"component", "payment_receiver",
			"invoice_id", inv.ID,
			"external_ref", ref,
		)
		return "invoice", nil
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}

	if ord, err := r.orders.GetByExternalRef(ctx, ref); err == nil && ord != nil {
		if ord.Status == domain.OrderStatusPaid {
			return "order", nil
		}
		extRef := ref
		if err := r.orders.UpdateStatus(ctx, ord.ID, domain.OrderStatusPaid, &extRef); err != nil {
			return "order", err
		}
		observability.FromContext(ctx).Info("order marked paid",
			"component", "payment_receiver",
			"order_id", ord.ID,
			"external_ref", ref,
		)
		// Refresh + handoff (email, ticket, admin notify) — não bloqueia.
		if refreshed, err := r.orders.GetByID(ctx, ord.ID); err == nil && refreshed != nil {
			r.onOrderPaid(ctx, refreshed)
			r.referralPayout(ctx, refreshed)
		}
		return "order", nil
	}
	return "", nil
}

// referralPayout dispara GrantOnFirstPaidOrder se houver ReferralService.
// Best-effort com log warn — erros não derrubam a transição já confirmada.
func (r *PaymentReceiver) referralPayout(ctx context.Context, ord *domain.Order) {
	if r.referrals == nil || ord == nil {
		return
	}
	if err := r.referrals.GrantOnFirstPaidOrder(ctx, ord); err != nil {
		observability.FromContext(ctx).Warn("referral payout failed",
			"component", "payment_receiver",
			"order_id", ord.ID,
			"error", err.Error(),
		)
	}
}

// MarkOrderPaid força a marcação direta (uso do admin via backoffice quando
// webhook não está configurado). Idempotente.
func (r *PaymentReceiver) MarkOrderPaid(ctx context.Context, orderID string) error {
	ord, err := r.orders.GetByID(ctx, orderID)
	if err != nil {
		return err
	}
	if ord.Status == domain.OrderStatusPaid {
		return nil
	}
	if err := r.orders.UpdateStatus(ctx, orderID, domain.OrderStatusPaid, ord.ExternalRef); err != nil {
		return err
	}
	if refreshed, err := r.orders.GetByID(ctx, ord.ID); err == nil && refreshed != nil {
		r.onOrderPaid(ctx, refreshed)
		r.referralPayout(ctx, refreshed)
	}
	return nil
}

// onOrderPaid agrupa todos os efeitos colaterais pós-pagamento:
//   1. Abre ticket pra categorias high-touch (recovery/BM/perfil)
//   2. Manda email de confirmação pro comprador
//   3. Dispara webhook administrativo (Slack/Discord)
//
// Cada passo é best-effort: falhas são logadas mas não revertem o pagamento.
// Comunicações com o cliente só rolam aqui — isso é o que evita spam, já
// que sem pagamento confirmado nada sai.
func (r *PaymentReceiver) onOrderPaid(ctx context.Context, ord *domain.Order) {
	plan, err := r.plans.GetByID(ctx, ord.PlanID)
	if err != nil || plan == nil {
		observability.FromContext(ctx).Warn("onOrderPaid: plan lookup failed",
			"order_id", ord.ID, "plan_id", ord.PlanID)
		return
	}
	r.maybeOpenTicket(ctx, ord, plan)
	r.sendConfirmationEmail(ctx, ord, plan)
	r.notifyAdmin(ctx, ord, plan)
	r.telegramBroadcast(ctx, ord, plan)
}

// telegramBroadcast dispara as notificações Telegram pós-paid:
//   - admin: template "checkout_paid_admin" pro chat configurado em
//     cfg.TelegramAdminChatID. Só dispara se TelegramNotifier setado E
//     adminChat preenchido.
//   - cliente: template "checkout_paid_customer" se user.Telegram != "".
//
// Best-effort: erro em qualquer step só vira warn no log; não bloqueia o
// MarkOrderPaid (que já voltou ack pro webhook caller).
func (r *PaymentReceiver) telegramBroadcast(ctx context.Context, ord *domain.Order, plan *domain.Plan) {
	if r.telegram == nil {
		return
	}
	vars := r.checkoutPaidVars(ctx, ord, plan)
	if r.adminTelegramTo != "" {
		if err := r.telegram.SendTelegram(ctx, r.adminTelegramTo, "checkout_paid_admin", vars); err != nil {
			observability.FromContext(ctx).Warn("telegram admin notify failed",
				"order_id", ord.ID, "error", err.Error())
		}
	}
	// Cliente opcional: precisa do user.Telegram preenchido (cadastrado no
	// register). Sem telegram = só email cobre.
	if r.users != nil {
		if u, err := r.users.GetByID(ctx, ord.UserID); err == nil && u != nil && u.Telegram != "" {
			if err := r.telegram.SendTelegram(ctx, u.Telegram, "checkout_paid_customer", vars); err != nil {
				observability.FromContext(ctx).Warn("telegram customer notify failed",
					"order_id", ord.ID, "error", err.Error())
			}
		}
	}
}

// checkoutPaidVars monta o map de substituição usado nos templates
// checkout_paid_* (email/telegram admin/cliente). Centralizado pra
// garantir consistência entre canais (nomes idênticos no template).
func (r *PaymentReceiver) checkoutPaidVars(ctx context.Context, ord *domain.Order, plan *domain.Plan) map[string]string {
	vars := map[string]string{
		"plan_name":           plan.Name,
		"settlement_amount":   ord.SettlementAmount,
		"settlement_currency": ord.SettlementCurrency,
		"display_amount":      ord.DisplayAmount,
		"display_currency":    ord.DisplayCurrency,
		"order_short_id":      shortID(ord.ID),
		"order_id":            ord.ID,
		"site_url":            strings.TrimRight(r.siteURL, "/"),
		"category":            plan.Category,
	}
	if r.users != nil {
		if u, err := r.users.GetByID(ctx, ord.UserID); err == nil && u != nil {
			vars["name"] = u.Name
			vars["customer_email"] = u.Email
		}
	}
	return vars
}

func shortID(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// maybeOpenTicket abre ticket de suporte automaticamente para categorias
// com handoff manual (Account Recovery, BMs, perfis). Idempotente — só abre
// se o pedido ainda não tem ticket_id. Falhas não bloqueiam a confirmação
// (logamos e seguimos).
func (r *PaymentReceiver) maybeOpenTicket(ctx context.Context, ord *domain.Order, plan *domain.Plan) {
	if ord.TicketID != nil && *ord.TicketID != "" {
		return
	}
	if r.tickets == nil {
		return
	}
	if !CategoriesOpeningTicket[plan.Category] {
		return
	}

	subject := fmt.Sprintf("[%s] Order #%s — %s", plan.Category, ord.ID[:8], plan.Name)
	body := r.formatTicketBody(ord, plan)

	t, err := r.tickets.Open(ctx, OpenTicketInput{
		UserID:  ord.UserID,
		Subject: subject,
		Body:    body,
		OrderID: &ord.ID,
	})
	if err != nil {
		observability.FromContext(ctx).Error("auto-open ticket failed",
			"component", "payment_receiver",
			"order_id", ord.ID,
			"category", plan.Category,
			"error", err.Error(),
		)
		return
	}
	if err := r.orders.LinkTicket(ctx, ord.ID, t.ID); err != nil {
		observability.FromContext(ctx).Warn("ticket linked but order LinkTicket failed",
			"order_id", ord.ID,
			"ticket_id", t.ID,
			"error", err.Error(),
		)
		return
	}
	observability.FromContext(ctx).Info("ticket auto-opened",
		"component", "payment_receiver",
		"order_id", ord.ID,
		"ticket_id", t.ID,
		"category", plan.Category,
	)
}

// formatTicketBody monta o corpo inicial do ticket: dados do pedido + dump
// do form (CustomData) em chave=valor, ordenado pra ficar legível pro admin.
func (r *PaymentReceiver) formatTicketBody(ord *domain.Order, plan *domain.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Order #%s — %s\n", ord.ID, plan.Name)
	fmt.Fprintf(&b, "Category: %s\n", plan.Category)
	fmt.Fprintf(&b, "Amount: %s %s (display %s %s)\n",
		ord.SettlementAmount, ord.SettlementCurrency,
		ord.DisplayAmount, ord.DisplayCurrency)
	if ord.ProfileID != nil {
		fmt.Fprintf(&b, "Profile: %s\n", *ord.ProfileID)
	}
	if ord.PublicationURL != nil && *ord.PublicationURL != "" {
		fmt.Fprintf(&b, "Publication URL: %s\n", *ord.PublicationURL)
	}
	if len(ord.CustomData) > 0 {
		b.WriteString("\nForm data:\n")
		keys := make([]string, 0, len(ord.CustomData))
		for k := range ord.CustomData {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := ord.CustomData[k]
			// Strings ficam inline; objetos/listas saem em JSON compacto.
			switch vv := v.(type) {
			case string:
				if vv == "" {
					continue
				}
				fmt.Fprintf(&b, "  %s: %s\n", k, vv)
			default:
				raw, _ := json.Marshal(vv)
				fmt.Fprintf(&b, "  %s: %s\n", k, string(raw))
			}
		}
	}
	return b.String()
}

// sendConfirmationEmail manda recibo + próximos passos pro comprador.
// Categorias de handoff manual ganham um corpo dedicado (ticket aberto);
// categorias automáticas (followers, likes, etc.) ganham um recibo simples.
// Best-effort: erro só vai pro log.
func (r *PaymentReceiver) sendConfirmationEmail(ctx context.Context, ord *domain.Order, plan *domain.Plan) {
	if r.email == nil || r.users == nil {
		return
	}
	u, err := r.users.GetByID(ctx, ord.UserID)
	if err != nil || u == nil || u.Email == "" {
		return
	}

	// PHASE-8 Wave 3: quando o EmailSender é o viralefy_sender (via
	// senderclient.Client), ele implementa TemplatedEmailer e renderiza o
	// template "checkout_paid" centralmente. Caímos no template path; só o
	// fallback legacy (SMTP/Resend direto) continua com HTML/Text hand-written.
	if tmpl, ok := r.email.(TemplatedEmailer); ok {
		vars := r.checkoutPaidVars(ctx, ord, plan)
		if err := tmpl.SendTemplate(ctx, u.Email, "checkout_paid", vars); err != nil {
			observability.FromContext(ctx).Warn("checkout_paid template email failed",
				"order_id", ord.ID, "error", err.Error())
		}
		return
	}

	manualHandoff := CategoriesOpeningTicket[plan.Category]
	accountURL := strings.TrimRight(r.siteURL, "/") + "/account"

	var subject, text, html string
	if manualHandoff {
		subject = fmt.Sprintf("Payment received — Order #%s (%s)", ord.ID[:8], plan.Name)
		text = fmt.Sprintf(`Hi %s,

We received your payment for "%s" (Order #%s).

A support ticket was opened automatically with the details you sent. Our team will follow up there shortly.

Track the ticket and conversation: %s

— Viralefy team
`, u.Name, plan.Name, ord.ID[:8], accountURL)
		html = fmt.Sprintf(`<p>Hi %s,</p>
<p>We received your payment for <strong>%s</strong> (Order <code>#%s</code>).</p>
<p>A support ticket was opened automatically with the details you sent. Our team will follow up there shortly.</p>
<p><a href="%s">Track the ticket in your account</a>.</p>
<p>— Viralefy team</p>`, u.Name, plan.Name, ord.ID[:8], accountURL)
	} else {
		subject = fmt.Sprintf("Payment received — Order #%s", ord.ID[:8])
		text = fmt.Sprintf(`Hi %s,

Payment received for "%s" (Order #%s). Delivery starts within 30 minutes.

Track the order: %s

— Viralefy team
`, u.Name, plan.Name, ord.ID[:8], accountURL)
		html = fmt.Sprintf(`<p>Hi %s,</p>
<p>Payment received for <strong>%s</strong> (Order <code>#%s</code>). Delivery starts within 30 minutes.</p>
<p><a href="%s">Track the order in your account</a>.</p>
<p>— Viralefy team</p>`, u.Name, plan.Name, ord.ID[:8], accountURL)
	}

	if err := r.email.Send(ctx, EmailMessage{
		To:       u.Email,
		Subject:  subject,
		TextBody: text,
		HTMLBody: html,
	}); err != nil {
		observability.FromContext(ctx).Warn("confirmation email failed",
			"order_id", ord.ID, "error", err.Error())
	}
}

// notifyAdmin dispara webhook administrativo (Slack/Discord) quando o
// pedido é high-touch (recovery/BM/perfil). Para categorias automáticas,
// não enche o canal — admin só precisa ser notificado do que precisa de
// atenção manual. Best-effort.
func (r *PaymentReceiver) notifyAdmin(ctx context.Context, ord *domain.Order, plan *domain.Plan) {
	if r.notifier == nil || !r.notifier.Enabled() {
		return
	}
	if !CategoriesOpeningTicket[plan.Category] {
		return
	}
	ticketLink := strings.TrimRight(r.siteURL, "/") + "/account"
	if ord.TicketID != nil && *ord.TicketID != "" {
		ticketLink = strings.TrimRight(r.siteURL, "/") + "/tickets/" + *ord.TicketID
	}
	text := fmt.Sprintf("🔔 New paid order in *%s*\n• Plan: %s\n• Amount: %s %s\n• Order: `#%s`\n• Triage: %s",
		plan.Category,
		plan.Name,
		ord.SettlementAmount, ord.SettlementCurrency,
		ord.ID[:8],
		ticketLink,
	)
	if err := r.notifier.Send(ctx, text); err != nil {
		observability.FromContext(ctx).Warn("admin webhook failed",
			"order_id", ord.ID, "error", err.Error())
	}
}
