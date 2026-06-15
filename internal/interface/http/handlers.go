package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/email"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/jwtkeys"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/payment"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/paymentsclient"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/turnstile"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
)

type Handlers struct {
	Plans           *application.PlanService
	Checkout        *application.CheckoutService
	Gateways        *application.GatewayService
	Auth            *application.AuthService
	UserAuth        *application.UserAuthService
	Currencies      *application.CurrencyService
	Categories      domain.CategoryRepository
	Orders          domain.OrderRepository
	Users           domain.UserRepository
	Tickets         *application.TicketService
	Profiles        *application.ProfileService
	Credits         *application.CreditService
	Invoices        *application.InvoiceService
	PaymentReceiver *application.PaymentReceiver
	Turnstile       *turnstile.Service
	Audit           *application.AuditService
	// DB é exposto pra middleware de idempotency (lê/escreve em
	// idempotency_keys). Quase nenhum handler precisa, mas o pattern de
	// passar via Handlers mantém os middlewares chainable.
	DB         *postgres.DB
	Metrics    *application.MetricCaptureService
	Reviews    *application.ReviewService
	EmailRepu  *application.EmailReputationService
	Coupons    *application.CouponService
	OrderSvc   *application.OrderService
	Notifs     *application.UserNotifService
	UserData   *application.UserDataService
	CountryPPP domain.CountryPPPRepository
	Referrals     *application.ReferralService
	ABTests       *application.ABTestService
	Fraud         *application.FraudService
	Refunds       *application.RefundService
	Subscriptions *application.SubscriptionService
	TaxRates      domain.TaxRateRepository
	Tax           *application.TaxService
	WhatsApp      *application.WhatsAppService
	Vendors       *application.VendorService
	APIKeys       *application.APIKeyService
	Events        *application.UserEventService
	// Honeypot — repo opcional pra ocultar superadmin de admins normais
	// + logar tentativas pra superadmin auditar.
	Honeypot      domain.AdminHoneypotRepository
	// Consent — audit log de decisões de cookie consent (LGPD Art. 8 §6).
	// Quando nil, POST /v1/me/consent vira 503 (best-effort) e o backend
	// nem expõe a rota (router.go filtra).
	Consent       *application.UserConsentService
	// Email sender — usado por handlers que precisam disparar transactional
	// email fora do fluxo de PaymentReceiver (ex.: AdminProofDecision quando
	// admin rejeita o comprovante e o cliente precisa ser avisado pra
	// reanexar).
	Email application.EmailSender
	// Storage S3-compat (MinIO local / Cloudflare R2). Quando disabled
	// (NoopStorage), o proof upload cai no fluxo legado base64 inline.
	Storage application.ObjectStorage
	// AdminTwoFA — 2FA service pra principais admin. Setado quando
	// TWOFA_ENCRYPTION_KEY presente no env. Nil = endpoints retornam 503
	// e AuthService.Login não bloqueia em partial_token.
	AdminTwoFA *application.TwoFAService
	// UserTwoFA — espelho pra usuários. Opcional pro user (Login não
	// bloqueia se não enrolled). Nag controlado pelo prompt logic.
	UserTwoFA *application.TwoFAService
	// MethodsRemote — quando setado (modo microservice PHASE-8 Wave 3),
	// PublicListPaymentMethods pula h.Checkout.ListPaymentMethods e proxy
	// direto pro viralefy_payments via paymentsclient. Nil = modo legado
	// (CheckoutService resolve in-memory).
	MethodsRemote *paymentsclient.Client
}

// clientIP extrai o IP do cliente do request, respeitando X-Forwarded-For
// quando vier do Caddy/Cloudflare. Usado pelo Turnstile pra reforçar
// detecção de bot via origem.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Pega o primeiro IP (o cliente original) — o resto são proxies.
		if idx := strings.IndexByte(xff, ','); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if ra := r.RemoteAddr; ra != "" {
		// "host:port" → "host"
		if idx := strings.LastIndexByte(ra, ':'); idx > 0 {
			return ra[:idx]
		}
		return ra
	}
	return ""
}

// --- Público ---

func (h *Handlers) ListPublicPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.Plans.ListPublic(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	// Enriquece com aggregateRating por plano — front emite no JSON-LD do
	// Product e do AggregateOffer. N+1 queries é aceitável aqui: catálogo
	// total < 100 planos e essa rota é cacheada (s-maxage no Caddy).
	if h.Reviews != nil {
		for i := range plans {
			if agg, err := h.Reviews.AggregateByPlan(r.Context(), plans[i].ID); err == nil && agg != nil {
				plans[i].AggregateRating = agg
			}
		}
	}
	writeData(w, http.StatusOK, plans)
}

func (h *Handlers) ListCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := h.Categories.ListActive(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, cats)
}

func (h *Handlers) ListCurrencies(w http.ResponseWriter, r *http.Request) {
	curs, err := h.Currencies.ListDisplayable(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, curs)
}

// CreateRecoveryRequest é o entrypoint para o formulário de Account Recovery
// nas LPs por país. Valida Turnstile, encontra o plano de recuperação, e
// dispara o Checkout com o snapshot completo do form em CustomData.
//
// O ticket é aberto automaticamente após a confirmação do pagamento (hook
// no PaymentReceiver). Pré-pagamento, fica só a order pending.
func (h *Handlers) CreateRecoveryRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Handle             string         `json:"handle"`               // @handle alvo
		Platform           string         `json:"platform"`             // instagram | tiktok
		BanDate            string         `json:"ban_date"`             // ISO 8601 ou texto livre
		EstimatedReason    string         `json:"estimated_reason"`     // suspeita do usuário
		LastPublicationURL string         `json:"last_publication_url"` // último post visível
		Description        string         `json:"description"`          // contexto extra
		ContactEmail       string         `json:"contact_email"`
		ContactName        string         `json:"contact_name"`
		DisplayCurrency    string         `json:"display_currency"`
		PaymentMethod      string         `json:"payment_method,omitempty"`
		TurnstileToken     string         `json:"turnstile_token"`
		Tracking           map[string]any `json:"tracking,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if body.Handle == "" || body.ContactEmail == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if h.Turnstile != nil {
		if err := h.Turnstile.Verify(r.Context(), body.TurnstileToken, clientIP(r)); err != nil {
			observability.FromContext(r.Context()).Warn("recovery: turnstile failed",
				"ip", clientIP(r),
				"error", err.Error(),
			)
			writeError(w, domain.ErrInvalidInput)
			return
		}
	}

	// Encontra o plano de recuperação na categoria dedicada.
	plans, err := h.Plans.ListByCategory(r.Context(), "recuperacao_perfil")
	if err != nil || len(plans) == 0 {
		writeError(w, domain.ErrNotFound)
		return
	}
	plan := plans[0]

	custom := map[string]any{
		"handle":               body.Handle,
		"platform":             body.Platform,
		"ban_date":             body.BanDate,
		"estimated_reason":     body.EstimatedReason,
		"last_publication_url": body.LastPublicationURL,
		"description":          body.Description,
		"contact_email":        body.ContactEmail,
		"contact_name":         body.ContactName,
		"form_type":            "account_recovery",
	}

	in := application.CheckoutInput{
		PlanID:          plan.ID,
		Email:           body.ContactEmail,
		Name:            body.ContactName,
		DisplayCurrency: body.DisplayCurrency,
		PublicationURL:  body.LastPublicationURL,
		PaymentMethod:   body.PaymentMethod,
		CustomData:      custom,
		Tracking:        h.enrichTracking(r, body.Tracking),
	}
	if uid := userIDFromContext(r.Context()); uid != "" {
		in.UserID = uid
	}
	res, err := h.Checkout.Checkout(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, res)
}

func (h *Handlers) CreateCheckout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlanID          string                        `json:"plan_id"`
		Email           string                        `json:"email"`
		Name            string                        `json:"name"`
		DisplayCurrency string                        `json:"display_currency"`
		ProfileID       string                        `json:"profile_id,omitempty"`
		NewProfile      *application.NewProfileInline `json:"new_profile,omitempty"`
		PublicationURL  string                        `json:"publication_url,omitempty"`
		PaymentMethod   string                        `json:"payment_method,omitempty"` // gateway | credits
		CustomData      map[string]any                `json:"custom_data,omitempty"`
		Tracking        map[string]any                `json:"tracking,omitempty"`
		CouponCode      string                        `json:"coupon_code,omitempty"`
		Country         string                        `json:"country,omitempty"`        // país do COMPRADOR (VAT)
		TargetCountry   string                        `json:"target_country,omitempty"` // mercado da entrega (LP)
		GatewayID       string                        `json:"gateway_id,omitempty"`     // método escolhido na UI nova
		PayCurrency     string                        `json:"pay_currency,omitempty"`   // pra Heleket/Stripe multi-currency
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	in := application.CheckoutInput{
		PlanID:          body.PlanID,
		Email:           body.Email,
		Name:            body.Name,
		DisplayCurrency: body.DisplayCurrency,
		ProfileID:       body.ProfileID,
		NewProfile:      body.NewProfile,
		PublicationURL:  body.PublicationURL,
		PaymentMethod:   body.PaymentMethod,
		CustomData:      body.CustomData,
		Tracking:        h.enrichTracking(r, body.Tracking),
		CouponCode:      body.CouponCode,
		Country:         body.Country,
		TargetCountry:   body.TargetCountry,
		GatewayID:       body.GatewayID,
		PayCurrency:     body.PayCurrency,
	}
	// Se houver token de usuário, força o userID do token (rota /v1/checkout é
	// pública mas honra a autenticação opcional para credit/profile linkage).
	if uid := userIDFromContext(r.Context()); uid != "" {
		in.UserID = uid
	}
	res, err := h.Checkout.Checkout(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, res)
}

// --- Auth de usuário ---

func (h *Handlers) UserRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email          string         `json:"email"`
		Name           string         `json:"name"`
		Password       string         `json:"password"`
		Phone          string         `json:"phone,omitempty"`
		Telegram       string         `json:"telegram,omitempty"`
		TurnstileToken string         `json:"turnstile_token"`
		Tracking       map[string]any `json:"tracking,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if !h.verifyTurnstile(r, body.TurnstileToken) {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	res, err := h.UserAuth.Register(r.Context(), application.RegisterInput{
		Email:    body.Email,
		Name:     body.Name,
		Password: body.Password,
		Phone:    body.Phone,
		Telegram: body.Telegram,
		Tracking: h.enrichTracking(r, body.Tracking),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, res)
}

func (h *Handlers) UserLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if !h.verifyTurnstile(r, body.TurnstileToken) {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	res, err := h.UserAuth.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, res)
}

// enrichTracking pega o snapshot client-side (utm/fbclid/gclid/referrer/
// landing_url/client_id/etc.) e adiciona campos server-side que o cliente
// não consegue forjar: IP real, user-agent visto pela API, e timestamp
// de submit. Cliente vazio também é OK — voltamos só com server-side.
//
// Tudo num único map[string]any pra ir direto pra orders.tracking jsonb.
func (h *Handlers) enrichTracking(r *http.Request, client map[string]any) map[string]any {
	out := make(map[string]any, len(client)+4)
	for k, v := range client {
		out[k] = v
	}
	out["server_ip"] = clientIP(r)
	if ua := r.Header.Get("User-Agent"); ua != "" {
		out["server_user_agent"] = ua
	}
	if al := r.Header.Get("Accept-Language"); al != "" {
		out["server_accept_language"] = al
	}
	out["server_submitted_at"] = time.Now().UTC().Format(time.RFC3339)
	return out
}

// verifyTurnstile valida o token contra o Cloudflare Turnstile. Quando o
// serviço está desabilitado (TURNSTILE_SECRET_KEY vazio em HML), aceita
// qualquer token (no-op). Retorna true se passou.
func (h *Handlers) verifyTurnstile(r *http.Request, token string) bool {
	if h.Turnstile == nil || !h.Turnstile.Enabled() {
		return true
	}
	if err := h.Turnstile.Verify(r.Context(), token, clientIP(r)); err != nil {
		observability.FromContext(r.Context()).Warn("turnstile failed on auth",
			"path", r.URL.Path,
			"ip", clientIP(r),
			"error", err.Error(),
		)
		return false
	}
	return true
}

// --- Tickets do usuário (loja) --- //

func (h *Handlers) MeListTickets(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	list, err := h.Tickets.ListForUser(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// MeOpenTicketsCount alimenta o badge "💬 (N)" do Header. Conta tickets
// em status open ou pending (que exigem ação do user ou do suporte).
func (h *Handlers) MeOpenTicketsCount(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	n, err := h.Tickets.CountOpenForUser(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]int{"open": n})
}

func (h *Handlers) MeCreateTicket(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	var body struct {
		Subject string  `json:"subject"`
		Body    string  `json:"body"`
		OrderID *string `json:"order_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	t, err := h.Tickets.Open(r.Context(), application.OpenTicketInput{
		UserID: userID, Subject: body.Subject, Body: body.Body, OrderID: body.OrderID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, t)
}

func (h *Handlers) MeGetTicket(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	d, err := h.Tickets.GetForUser(r.Context(), chi.URLParam(r, "id"), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, d)
}

func (h *Handlers) MeReplyTicket(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Tickets.ReplyAsUser(r.Context(), chi.URLParam(r, "id"), userID, body.Body); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Tickets (admin) --- //

func (h *Handlers) AdminListTickets(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	list, err := h.Tickets.AdminList(r.Context(), status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

func (h *Handlers) AdminGetTicket(w http.ResponseWriter, r *http.Request) {
	d, err := h.Tickets.AdminGet(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, d)
}

func (h *Handlers) AdminReplyTicket(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Tickets.ReplyAsAdmin(r.Context(), chi.URLParam(r, "id"), p.AdminID, body.Body); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) AdminUpdateTicket(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Status   *string `json:"status,omitempty"`
		Priority *string `json:"priority,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if body.Status != nil {
		if err := h.Tickets.AdminUpdateStatus(r.Context(), id, domain.TicketStatus(*body.Status)); err != nil {
			writeError(w, err)
			return
		}
	}
	if body.Priority != nil {
		if err := h.Tickets.AdminUpdatePriority(r.Context(), id, domain.TicketPriority(*body.Priority)); err != nil {
			writeError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) MeOrders(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	orders, err := h.Orders.ListViewByUser(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, orders)
}

// --- Admin ---

// AdminMe devolve o principal autenticado (papel + permissões) para o
// backoffice adaptar a UI (esconder ações sem permissão).
func (h *Handlers) AdminMe(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	writeData(w, http.StatusOK, p)
}

// AdminBecomeCustomer cria (se necessário) um user record com o mesmo
// email/name do admin logado e devolve uma UserSession. Permite ao admin
// abrir o lado de customer sem precisar de outro registro/login.
//
// Não é login impersonation — é provisionamento idempotente de um shadow
// account paralelo. O password gerado (apenas no PRIMEIRO chamado) é
// devolvido junto pro admin guardar se quiser usar /login normalmente
// depois.
func (h *Handlers) AdminBecomeCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	adminRow, err := h.Auth.GetAdminByID(r.Context(), p.AdminID)
	if err != nil || adminRow == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	sess, generatedPwd, err := h.UserAuth.EnsureShadowAccount(r.Context(), adminRow.Email, adminRow.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"session":            sess,
		"generated_password": generatedPwd, // vazio se user já existia
	})
}

func (h *Handlers) AdminListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.Auth.Roles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, roles)
}

// adminPublicView é o shape devolvido aos handlers /v1/admin/admins —
// password_hash NUNCA é incluído (nem em logs nem em wire).
type adminPublicView struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	RequiresTwoFA bool      `json:"requires_2fa"`
	CreatedAt     time.Time `json:"created_at"`
}

func toAdminView(a domain.Admin) adminPublicView {
	return adminPublicView{
		ID: a.ID, Email: a.Email, Name: a.Name, Role: a.Role,
		RequiresTwoFA: a.RequiresTwoFA, CreatedAt: a.CreatedAt,
	}
}

// AdminListAdmins — GET /v1/admin/admins
//
// Honeypot policy (2026-06-11):
//
//   Quando o caller NÃO é superadmin:
//     1. Toda row com role=superadmin é MASCARADA pra role="manager"
//        (camuflagem — admin nem suspeita que tem alguém com mais poder).
//     2. Qualquer superadmin que esse caller já "shadow-deletou"
//        (admin_honeypot_log com action='delete') é EXCLUÍDO da resposta
//        (admin acha que apagou pra valer).
//     3. Self entry: o admin sempre aparece com a própria role real
//        (que NÃO é superadmin, então não cai no mascaramento).
//
//   Quando o caller É superadmin: tudo passa cru — vê todos com role
//   real, incluindo outros superadmins. Sem mascaramento, sem exclusão.
func (h *Handlers) AdminListAdmins(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromContext(r.Context())
	list, err := h.Auth.AdminListAdmins(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}

	// Path superadmin — passa tudo cru.
	if caller.Role == domain.RoleSuperadmin {
		out := make([]adminPublicView, 0, len(list))
		for _, a := range list {
			out = append(out, toAdminView(a))
		}
		writeData(w, http.StatusOK, out)
		return
	}

	// Path admin normal — mascarar + filtrar shadow-deleted.
	shadowDeletedIDs := map[string]bool{}
	if h.Honeypot != nil {
		ids, _ := h.Honeypot.ActorShadowDeletedTargets(r.Context(), caller.AdminID)
		for _, id := range ids {
			shadowDeletedIDs[id] = true
		}
	}

	out := make([]adminPublicView, 0, len(list))
	for _, a := range list {
		// Self → renderiza com role real do caller.
		if a.ID == caller.AdminID {
			out = append(out, toAdminView(a))
			continue
		}
		// Shadow-deleted por este caller? Pula.
		if shadowDeletedIDs[a.ID] {
			continue
		}
		// Mascarar superadmin → manager pra esconder hierarquia real.
		if a.Role == domain.RoleSuperadmin {
			masked := a
			masked.Role = "manager"
			out = append(out, toAdminView(masked))
			continue
		}
		out = append(out, toAdminView(a))
	}
	writeData(w, http.StatusOK, out)
}

// AdminCreateAdmin — POST /v1/admin/admins
// Body: { email, name, role }. Devolve { admin, generated_password } —
// senha gerada UMA vez (admin promotor anota e entrega).
func (h *Handlers) AdminCreateAdmin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	// Só superadmin cria outro superadmin (defesa em profundidade — a
	// camada de service também valida).
	caller, _ := principalFromContext(r.Context())
	if body.Role == domain.RoleSuperadmin && caller.Role != domain.RoleSuperadmin {
		writeError(w, domain.ErrForbidden)
		return
	}
	created, pwd, err := h.Auth.AdminCreate(r.Context(), body.Email, body.Name, body.Role)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(r.Context(), application.AuditEntry{
			ActorType:  "admin",
			ActorID:    caller.AdminID,
			Action:     "admin.create",
			TargetType: "admin",
			TargetID:   created.ID,
			Metadata: map[string]any{
				"email": created.Email,
				"role":  created.Role,
			},
		})
	}
	writeData(w, http.StatusCreated, map[string]any{
		"admin":              toAdminView(*created),
		"generated_password": pwd,
	})
}

// AdminUpdateAdmin — PUT /v1/admin/admins/{id}
//
// Honeypot: quando caller != superadmin tenta editar um superadmin,
// NÃO retorna 403 (que revelaria a existência da hierarquia). Em vez disso:
//   1. Grava admin_honeypot_log entry (action='update_role')
//   2. Retorna 200 com adminPublicView FALSA (role mascarada como
//      "manager" + nova role do payload aplicada visualmente)
//   3. Persistência real do role do superadmin NÃO acontece — ele
//      continua superadmin no DB.
//
// Pro superadmin de verdade que olha o painel, nada muda. Pro admin
// malicioso, tudo parece ter funcionado.
func (h *Handlers) AdminUpdateAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	caller, _ := principalFromContext(r.Context())

	// Honeypot path — caller não é superadmin tentando mexer num target
	// que é superadmin. Detecta via lookup do target.
	if caller.Role != domain.RoleSuperadmin {
		target, _ := h.Auth.GetAdminByID(r.Context(), id)
		if target != nil && target.Role == domain.RoleSuperadmin {
			h.logHoneypot(r.Context(), caller.AdminID, id, domain.HoneypotActionUpdateRole, body.Role, r)
			// Retorna view falsa — role que o admin tentou aplicar.
			fake := *target
			fake.Role = body.Role
			writeData(w, http.StatusOK, toAdminView(fake))
			return
		}
	}

	if err := h.Auth.AdminUpdateRole(r.Context(), caller, id, body.Role); err != nil {
		writeError(w, err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(r.Context(), application.AuditEntry{
			ActorType:  "admin",
			ActorID:    caller.AdminID,
			Action:     "admin.update_role",
			TargetType: "admin",
			TargetID:   id,
			Metadata:   map[string]any{"new_role": body.Role},
		})
	}
	updated, err := h.Auth.GetAdminByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, toAdminView(*updated))
}

// AdminDeleteAdmin — DELETE /v1/admin/admins/{id}
//
// Honeypot: caller não-superadmin tentando apagar superadmin → grava
// entry no admin_honeypot_log com action='delete' (que também serve de
// shadow-delete state: na próxima listagem, esse caller não vê mais o
// target) e retorna 200 "status: deleted" sem tocar no DB.
func (h *Handlers) AdminDeleteAdmin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	caller, _ := principalFromContext(r.Context())

	if caller.Role != domain.RoleSuperadmin {
		target, _ := h.Auth.GetAdminByID(r.Context(), id)
		if target != nil && target.Role == domain.RoleSuperadmin {
			h.logHoneypot(r.Context(), caller.AdminID, id, domain.HoneypotActionDelete, "", r)
			writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
			return
		}
	}

	if err := h.Auth.AdminDelete(r.Context(), caller, id); err != nil {
		writeError(w, err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(r.Context(), application.AuditEntry{
			ActorType:  "admin",
			ActorID:    caller.AdminID,
			Action:     "admin.delete",
			TargetType: "admin",
			TargetID:   id,
		})
	}
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// logHoneypot — helper privado que grava tentativa de admin malicioso no
// admin_honeypot_log. Best-effort: erro de gravação NÃO bloqueia a fake
// response (a UX do attacker tem que ser idêntica à path real).
func (h *Handlers) logHoneypot(ctx context.Context, actorID, targetID, action, attemptedRole string, r *http.Request) {
	if h.Honeypot == nil {
		return
	}
	entry := domain.AdminHoneypotEntry{
		ID:            uuid.NewString(),
		ActorAdminID:  actorID,
		TargetAdminID: targetID,
		Action:        action,
		Metadata: map[string]any{
			"ip":         clientIP(r),
			"user_agent": r.Header.Get("User-Agent"),
		},
	}
	if attemptedRole != "" {
		entry.AttemptedRole = &attemptedRole
	}
	_ = h.Honeypot.Record(ctx, entry)
}

// AdminListHoneypot — GET /v1/admin/honeypot (RequireSuperadmin)
//
// Devolve tentativas registradas pra superadmin auditar. Hidratado com
// email/name de actor + target. Limit default 200.
func (h *Handlers) AdminListHoneypot(w http.ResponseWriter, r *http.Request) {
	if h.Honeypot == nil {
		writeData(w, http.StatusOK, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	list, err := h.Honeypot.ListAll(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

func (h *Handlers) AdminLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if !h.verifyTurnstile(r, body.TurnstileToken) {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	res, err := h.Auth.Login(r.Context(), application.LoginInput{Email: body.Email, Password: body.Password})
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, res)
}

func (h *Handlers) AdminListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.Plans.ListAdmin(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, plans)
}

func (h *Handlers) AdminCreatePlan(w http.ResponseWriter, r *http.Request) {
	var body application.CreatePlanInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	p, err := h.Plans.Create(r.Context(), body)
	if err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "create", "plan", p.ID, nil, p)
	writeData(w, http.StatusCreated, p)
}

func (h *Handlers) AdminUpdatePlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body application.UpdatePlanInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	body.ID = id
	// Snapshot do plano antes do update (best-effort) — usado no diff.
	before, _ := h.Plans.GetByID(r.Context(), id)
	p, err := h.Plans.Update(r.Context(), body)
	if err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "update", "plan", p.ID, before, p)
	writeData(w, http.StatusOK, p)
}

func (h *Handlers) AdminDeletePlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	before, _ := h.Plans.GetByID(r.Context(), id)
	if err := h.Plans.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "delete", "plan", id, before, nil)
	w.WriteHeader(http.StatusNoContent)
}

// logAudit é um wrapper enxuto que recolhe actor (do contexto) + meta
// (IP, user-agent) e dispara o AuditService de forma não-bloqueante. Se
// AuditService não estiver configurado (HML sem migration), vira no-op.
func (h *Handlers) logAudit(r *http.Request, action, targetType, targetID string, before, after any) {
	h.logAuditMeta(r, action, targetType, targetID, before, after, nil)
}

// logAuditMeta é a versão que aceita metadata adicional além do default (IP,
// UA, path, method). Usado por ações que precisam carregar contexto pro
// post-mortem (ex.: AdminMarkPaid grava amount/currency/external_ref).
func (h *Handlers) logAuditMeta(r *http.Request, action, targetType, targetID string, before, after any, extra map[string]any) {
	if h.Audit == nil {
		return
	}
	actorType := "system"
	actorID := "system"
	if p, ok := principalFromContext(r.Context()); ok && p.AdminID != "" {
		actorType = "admin"
		actorID = p.AdminID
	}
	meta := map[string]any{
		"ip":         clientIP(r),
		"user_agent": r.Header.Get("User-Agent"),
		"path":       r.URL.Path,
		"method":     r.Method,
	}
	for k, v := range extra {
		meta[k] = v
	}
	h.Audit.Log(r.Context(), application.AuditEntry{
		ActorType:  actorType,
		ActorID:    actorID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Before:     before,
		After:      after,
		Metadata:   meta,
	})
}

func (h *Handlers) AdminListGateways(w http.ResponseWriter, r *http.Request) {
	list, err := h.Gateways.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

func (h *Handlers) AdminCreateGateway(w http.ResponseWriter, r *http.Request) {
	var body application.CreateGatewayInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	g, err := h.Gateways.Create(r.Context(), body)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, g)
}

func (h *Handlers) AdminUpdateGateway(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body application.UpdateGatewayInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	body.ID = id
	g, err := h.Gateways.Update(r.Context(), body)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, g)
}

func (h *Handlers) AdminDeleteGateway(w http.ResponseWriter, r *http.Request) {
	if err := h.Gateways.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) AdminListOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := h.Orders.ListAllView(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, orders)
}

// AdminGetOrder devolve um pedido específico com TUDO: custom_data,
// tracking, payment_extra. Hidrata profile (handle, display_name, platform)
// e user (name, email) pra UI mostrar nomes clicáveis em vez de UUIDs.
func (h *Handlers) AdminGetOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ord, err := h.Orders.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	// Estrutura de saída: order com profile{} e user{} embutidos. Nulos
	// se o lookup falhar (perfil deletado, user removido) — front exibe "—".
	out := map[string]any{"order": ord}
	if ord.ProfileID != nil && *ord.ProfileID != "" {
		if p, err := h.Profiles.GetByID(r.Context(), *ord.ProfileID); err == nil && p != nil {
			out["profile"] = map[string]any{
				"id":           p.ID,
				"handle":       p.Handle,
				"display_name": p.DisplayName,
				"platform":     p.Platform,
				"verified":     p.Verified,
			}
		}
	}
	if ord.UserID != "" {
		if u, err := h.Users.GetByID(r.Context(), ord.UserID); err == nil && u != nil {
			out["user"] = map[string]any{
				"id":    u.ID,
				"name":  u.Name,
				"email": u.Email,
			}
		}
	}
	writeData(w, http.StatusOK, out)
}

// AdminPatchOrder permite editar status e nota interna do pedido.
// Status: pending|paid|failed|cancelled. Mudança pra `paid` deveria usar
// /orders/{id}/mark-paid (que dispara os hooks pós-pagamento); aqui
// permitimos pra correção emergencial (não dispara email/webhook).
func (h *Handlers) AdminPatchOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Status *string `json:"status,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	before, _ := h.Orders.GetByID(r.Context(), id)
	if before == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	if body.Status != nil {
		valid := map[string]domain.OrderStatus{
			"pending":   domain.OrderStatusPending,
			"paid":      domain.OrderStatusPaid,
			"failed":    domain.OrderStatusFailed,
			"cancelled": domain.OrderStatusCancelled,
		}
		s, ok := valid[*body.Status]
		if !ok {
			writeError(w, domain.ErrInvalidInput)
			return
		}
		if err := h.Orders.UpdateStatus(r.Context(), id, s, before.ExternalRef); err != nil {
			writeError(w, err)
			return
		}
	}
	after, _ := h.Orders.GetByID(r.Context(), id)
	h.logAudit(r, "update", "order", id, before, after)
	writeData(w, http.StatusOK, after)
}

// AdminCaptureOrderMetrics dispara captura manual de baseline ou delivery
// pra um pedido. Síncrono (10–20s de scrape no máx) — admin clica e
// espera. Body opcional: {"kind":"baseline"|"delivery"} — default baseline.
func (h *Handlers) AdminCaptureOrderMetrics(w http.ResponseWriter, r *http.Request) {
	if h.Metrics == nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	id := chi.URLParam(r, "id")
	kind := "baseline"
	if body, err := io.ReadAll(r.Body); err == nil && len(body) > 0 {
		var b struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(body, &b)
		if b.Kind == "delivery" {
			kind = "delivery"
		}
	}
	var err error
	if kind == "delivery" {
		err = h.Metrics.CaptureDelivery(r.Context(), id)
	} else {
		err = h.Metrics.CaptureBaseline(r.Context(), id)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	// Devolve o order atualizado pra UI re-renderizar imediatamente.
	ord, _ := h.Orders.GetByID(r.Context(), id)
	writeData(w, http.StatusOK, ord)
}

// AdminMetricsSummary alimenta o /dashboard com agregados:
//   - totals por status (pending/paid/failed)
//   - revenue total em USD (settlement_amount somado quando paid)
//   - top 5 categorias por revenue
//   - top 5 países por revenue (extraído de plan_category, ou da
//     tracking — fora de escopo nesse primeiro corte)
//   - serie temporal de 30d (orders/dia)
//
// Tudo computado em memória — escala bem até ~50k orders. Em PRD pode
// virar materialized view.
func (h *Handlers) AdminMetricsSummary(w http.ResponseWriter, r *http.Request) {
	orders, err := h.Orders.ListAllView(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	type byCat struct {
		Category string `json:"category"`
		Orders   int    `json:"orders"`
		Revenue  string `json:"revenue_usd"`
	}
	type daily struct {
		Day     string `json:"day"`
		Orders  int    `json:"orders"`
		Revenue string `json:"revenue_usd"`
	}
	statusCount := map[string]int{}
	catAgg := map[string]struct {
		count   int
		revenue float64
	}{}
	dailyAgg := map[string]struct {
		count   int
		revenue float64
	}{}
	var totalRevenue float64
	var totalPaid int
	for _, o := range orders {
		statusCount[string(o.Status)]++
		amt := float64(o.AmountCents) / 100.0
		day := o.CreatedAt.UTC().Format("2006-01-02")
		dEntry := dailyAgg[day]
		dEntry.count++
		if o.Status == domain.OrderStatusPaid {
			dEntry.revenue += amt
			totalRevenue += amt
			totalPaid++
			cEntry := catAgg[o.PlanCategory]
			cEntry.count++
			cEntry.revenue += amt
			catAgg[o.PlanCategory] = cEntry
		}
		dailyAgg[day] = dEntry
	}

	// top 5 categorias por revenue desc
	cats := make([]byCat, 0, len(catAgg))
	for k, v := range catAgg {
		cats = append(cats, byCat{
			Category: k, Orders: v.count,
			Revenue: strings.TrimRight(strings.TrimRight(formatFloat(v.revenue, 2), "0"), "."),
		})
	}
	// sort manual sem importar "sort" — pequeno e claro
	for i := 1; i < len(cats); i++ {
		j := i
		for j > 0 && parseFloatOr(cats[j].Revenue, 0) > parseFloatOr(cats[j-1].Revenue, 0) {
			cats[j], cats[j-1] = cats[j-1], cats[j]
			j--
		}
	}
	if len(cats) > 5 {
		cats = cats[:5]
	}

	// série diária ordenada (últimos 30 dias)
	days := make([]string, 0, len(dailyAgg))
	for k := range dailyAgg {
		days = append(days, k)
	}
	// sort lexicográfico funciona pra YYYY-MM-DD
	for i := 1; i < len(days); i++ {
		j := i
		for j > 0 && days[j] < days[j-1] {
			days[j], days[j-1] = days[j-1], days[j]
			j--
		}
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")
	series := make([]daily, 0, 30)
	for _, d := range days {
		if d < cutoff {
			continue
		}
		entry := dailyAgg[d]
		series = append(series, daily{
			Day: d, Orders: entry.count,
			Revenue: formatFloat(entry.revenue, 2),
		})
	}

	writeData(w, http.StatusOK, map[string]any{
		"orders_total":   len(orders),
		"orders_paid":    totalPaid,
		"revenue_usd":    formatFloat(totalRevenue, 2),
		"status_count":   statusCount,
		"top_categories": cats,
		"daily_30d":      series,
	})
}

func formatFloat(f float64, dec int) string {
	if dec == 2 {
		return strconv.FormatFloat(f, 'f', 2, 64)
	}
	return strconv.FormatFloat(f, 'f', dec, 64)
}

func parseFloatOr(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return def
}

func (h *Handlers) AdminListCurrencies(w http.ResponseWriter, r *http.Request) {
	curs, err := h.Currencies.ListAll(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, curs)
}

func (h *Handlers) AdminUpdateCurrency(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	var body struct {
		Rate           float64 `json:"rate"`
		DisplayEnabled bool    `json:"display_enabled"`
		SettlementCode string  `json:"settlement_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	// ABAC: mudança grande de taxa (atributo) só por superadmin.
	if current, err := h.Currencies.Get(r.Context(), code); err == nil {
		if application.IsLargeRateChange(current.Rate, body.Rate) {
			if p, ok := principalFromContext(r.Context()); !ok || p.Role != domain.RoleSuperadmin {
				writeError(w, domain.ErrForbidden)
				return
			}
		}
	}
	c, err := h.Currencies.Update(r.Context(), application.UpdateCurrencyInput{
		Code: code, Rate: body.Rate, DisplayEnabled: body.DisplayEnabled, SettlementCode: body.SettlementCode,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, c)
}

// --- Perfis (loja, área logada) --- //

func (h *Handlers) MeListProfiles(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	list, err := h.Profiles.List(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

func (h *Handlers) MeAddProfile(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	var body struct {
		Platform    string `json:"platform"`
		Handle      string `json:"handle"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	p, err := h.Profiles.Add(r.Context(), application.AddProfileInput{
		UserID: userID, Platform: body.Platform, Handle: body.Handle, DisplayName: body.DisplayName,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, p)
}

func (h *Handlers) MeDeleteProfile(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if err := h.Profiles.Delete(r.Context(), chi.URLParam(r, "id"), userID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Créditos + ledger --- //

func (h *Handlers) MeCredits(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	acct, err := h.Credits.Balance(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, acct)
}

func (h *Handlers) MeTransactions(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	list, err := h.Credits.History(r.Context(), userID, 200)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// --- Invoices (recarga de créditos) --- //

func (h *Handlers) MeRecharge(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	var body struct {
		AmountCents     int64  `json:"amount_cents"`
		DisplayCurrency string `json:"display_currency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	// Precisamos do e-mail/nome do user para passar pro gateway.
	// Como o handler tem só o userID, pegamos via Orders.ListByUser? Não — o
	// jeito limpo é o service buscar via UserRepository. Pra evitar nova
	// dependência aqui no handler, devolvemos os dados via service que sabe ler.
	inv, err := h.Invoices.Create(r.Context(), application.CreateInvoiceInput{
		UserID:          userID,
		AmountCents:     body.AmountCents,
		DisplayCurrency: body.DisplayCurrency,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, inv)
}

func (h *Handlers) MeListInvoices(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	list, err := h.Invoices.ListForUser(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// --- Admin: invoices --- //

func (h *Handlers) AdminListInvoices(w http.ResponseWriter, r *http.Request) {
	list, err := h.Invoices.AdminListView(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

func (h *Handlers) AdminMarkInvoicePaid(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Snapshot do estado anterior pra audit (before). Se a invoice já estava
	// paga, AdminMarkPaid é no-op idempotente — ainda assim logamos a tentativa
	// (útil pra rastrear quem clicou "marcar como pago" repetido).
	before, _ := h.Invoices.AdminGet(r.Context(), id)
	inv, err := h.Invoices.AdminMarkPaid(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	// Metadata extra pro caso de incidente (ex.: order 450f0e6f) onde precisamos
	// reconstruir contexto sem caçar nas tabelas. external_ref vazio sinaliza
	// manual_pix sem comprovante de gateway — pista importante.
	extRef := ""
	if inv != nil && inv.ExternalRef != nil {
		extRef = *inv.ExternalRef
	}
	gwID := ""
	if inv != nil && inv.GatewayID != nil {
		gwID = *inv.GatewayID
	}
	meta := map[string]any{
		"invoice_id":         id,
		"user_id":            "",
		"amount_cents":       int64(0),
		"currency":           "",
		"settlement_amount":  int64(0),
		"settlement_currency": "",
		"gateway_id":         gwID,
		"external_ref":       extRef,
		"was_already_paid":   before != nil && before.Status == domain.InvoiceStatusPaid,
	}
	if inv != nil {
		meta["user_id"] = inv.UserID
		meta["amount_cents"] = inv.AmountCents
		meta["currency"] = inv.Currency
		meta["settlement_amount"] = inv.SettlementAmount
		meta["settlement_currency"] = inv.SettlementCurrency
	}
	h.logAuditMeta(r, "invoice.mark_paid", "invoice", id, before, inv, meta)
	writeData(w, http.StatusOK, inv)
}

// AdminGetInvoice devolve uma recarga específica com o user hidratado pra
// UI mostrar nome em vez do UUID.
func (h *Handlers) AdminGetInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	inv, err := h.Invoices.AdminGet(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	out := map[string]any{"invoice": inv}
	if u, err := h.Users.GetByID(r.Context(), inv.UserID); err == nil && u != nil {
		out["user"] = map[string]any{
			"id":    u.ID,
			"name":  u.Name,
			"email": u.Email,
		}
	}
	writeData(w, http.StatusOK, out)
}

// --- Webhooks (público, verificados por assinatura) --- //

func (h *Handlers) WooviWebhook(w http.ResponseWriter, r *http.Request) {
	logger := observability.FromContext(r.Context()).With("provider", "woovi")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("woovi", "bad_request").Inc()
		writeError(w, domain.ErrInvalidInput)
		return
	}
	gw, err := h.Gateways.GetActiveByProvider(r.Context(), "woovi")
	if err != nil || gw == nil {
		observability.GatewayCallbacksTotal.WithLabelValues("woovi", "no_gateway").Inc()
		writeError(w, domain.ErrNotFound)
		return
	}
	if err := payment.VerifyWooviWebhook(body, r.Header.Get("x-webhook-signature"), gw.Config["webhook_secret"]); err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("woovi", "invalid_signature").Inc()
		logger.Warn("webhook signature invalid", "error", err.Error())
		writeError(w, domain.ErrUnauthorized)
		return
	}
	ev, err := payment.ParseWooviEvent(body)
	if err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("woovi", "parse_error").Inc()
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if ev.IsPaid() {
		if _, err := h.PaymentReceiver.ConfirmByExternalRef(r.Context(), ev.Charge.Identifier); err != nil {
			observability.GatewayCallbacksTotal.WithLabelValues("woovi", "confirm_failed").Inc()
			logger.Error("ConfirmByExternalRef failed", "error", err.Error())
		} else {
			observability.GatewayCallbacksTotal.WithLabelValues("woovi", "confirmed").Inc()
		}
	} else {
		observability.GatewayCallbacksTotal.WithLabelValues("woovi", "ignored").Inc()
	}
	w.WriteHeader(http.StatusOK)
}

// StripeWebhook recebe eventos da Stripe (https://stripe.com/docs/webhooks).
// Signature em `Stripe-Signature` (HMAC SHA256 do `timestamp.payload` com
// o webhook secret — vide payment.VerifyStripeWebhook). Em
// checkout.session.completed dispara MarkOrderPaid pelo client_reference_id
// (que setamos como order_id no CreateCharge). PaymentReceiver é idempotente
// — Stripe re-entrega em caso de 5xx; segundo fire é no-op.
func (h *Handlers) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	logger := observability.FromContext(r.Context()).With("provider", "stripe")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("stripe", "bad_request").Inc()
		writeError(w, domain.ErrInvalidInput)
		return
	}
	gw, err := h.Gateways.GetActiveByProvider(r.Context(), "stripe")
	if err != nil || gw == nil {
		observability.GatewayCallbacksTotal.WithLabelValues("stripe", "no_gateway").Inc()
		writeError(w, domain.ErrNotFound)
		return
	}
	if err := payment.VerifyStripeWebhook(body, r.Header.Get("Stripe-Signature"), gw.Config["webhook_secret"]); err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("stripe", "invalid_signature").Inc()
		logger.Warn("webhook signature invalid", "error", err.Error())
		writeError(w, domain.ErrUnauthorized)
		return
	}
	ev, err := payment.ParseStripeEvent(body)
	if err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("stripe", "parse_error").Inc()
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if !ev.IsPaid() {
		// Ignora eventos que não sinalizam pagamento confirmado (charge.failed,
		// payment_intent.created etc). Stripe espera 2xx mesmo assim — senão
		// continua reentregando.
		observability.GatewayCallbacksTotal.WithLabelValues("stripe", "ignored").Inc()
		w.WriteHeader(http.StatusOK)
		return
	}
	// Idempotency check: Stripe re-entrega em 5xx; segunda chamada chega
	// antes da primeira terminar de processar. Insert FAIL-FAST se o event_id
	// já está registrado — evita double-fire de email/ticket. Race entre
	// duas requests do mesmo event_id é resolvida pelo unique constraint
	// (uma INSERT vence, a outra cai em conflict e ignora).
	//
	// Round 25 HIGH fix: ANTES, qualquer erro no INSERT (incluindo timeout
	// transitório de DB) caía no caminho "logger.Warn + segue pro
	// MarkOrderPaid" — comentário antigo dizia "MarkOrderPaid é idempotente
	// por status guard", mas o status guard é APÓS um SELECT+UPDATE dentro
	// do PaymentReceiver, e duas execuções concorrentes do mesmo event
	// ainda podem disparar side effects não-idempotentes (notifs, emails,
	// crédito). Comportamento correto:
	//
	//   - INSERT OK (rows=1)            → segue pro MarkOrderPaid
	//   - unique_violation (rows=0 OR
	//     pgErr 23505)                  → ACK 200, NÃO chama MarkOrderPaid
	//   - qualquer outro erro de DB     → 500, NÃO chama MarkOrderPaid;
	//                                     Stripe re-entrega e a tentativa
	//                                     seguinte tenta o INSERT de novo
	orderID := ev.OrderID()
	if h.DB != nil {
		tag, derr := h.DB.Pool().Exec(r.Context(),
			`INSERT INTO stripe_events_processed (event_id, event_type, order_id)
			 VALUES ($1, $2, NULLIF($3,''))
			 ON CONFLICT (event_id) DO NOTHING`,
			ev.ID, ev.Type, orderID)
		decision := classifyStripeIdempotencyResult(tag.RowsAffected(), derr)
		switch decision {
		case idempotencyProceed:
			// segue pro MarkOrderPaid abaixo
		case idempotencyDuplicate:
			observability.GatewayCallbacksTotal.WithLabelValues("stripe", "duplicate").Inc()
			logger.Info("stripe event duplicate", "event_id", ev.ID)
			w.WriteHeader(http.StatusOK)
			return
		case idempotencyTransientError:
			observability.GatewayCallbacksTotal.WithLabelValues("stripe", "idem_log_error").Inc()
			logger.Error("stripe events log insert failed; refusing to process to avoid double-fire",
				"event_id", ev.ID, "error", derr.Error())
			// 500 → Stripe re-entrega; tentativa futura pega o INSERT limpo.
			writeError(w, errors.New("idempotency log unavailable"))
			return
		}
	}
	if orderID == "" {
		observability.GatewayCallbacksTotal.WithLabelValues("stripe", "no_order_id").Inc()
		logger.Warn("stripe event missing order id", "event_id", ev.ID, "type", ev.Type)
		// 200 mesmo assim — o defeito é nosso ou de config; Stripe não pode
		// resolver com retry. Já caiu na métrica pra alarme.
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := h.PaymentReceiver.MarkOrderPaid(r.Context(), orderID); err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("stripe", "confirm_failed").Inc()
		logger.Error("MarkOrderPaid failed", "order_id", orderID, "error", err.Error())
	} else {
		observability.GatewayCallbacksTotal.WithLabelValues("stripe", "confirmed").Inc()
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handlers) HeleketWebhook(w http.ResponseWriter, r *http.Request) {
	logger := observability.FromContext(r.Context()).With("provider", "heleket")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("heleket", "bad_request").Inc()
		writeError(w, domain.ErrInvalidInput)
		return
	}
	gw, err := h.Gateways.GetActiveByProvider(r.Context(), "heleket")
	if err != nil || gw == nil {
		observability.GatewayCallbacksTotal.WithLabelValues("heleket", "no_gateway").Inc()
		writeError(w, domain.ErrNotFound)
		return
	}
	if err := payment.VerifyHeleketWebhook(body, gw.Config["api_key"]); err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("heleket", "invalid_signature").Inc()
		logger.Warn("webhook signature invalid", "error", err.Error())
		writeError(w, domain.ErrUnauthorized)
		return
	}
	ev, err := payment.ParseHeleketEvent(body)
	if err != nil {
		observability.GatewayCallbacksTotal.WithLabelValues("heleket", "parse_error").Inc()
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if ev.IsPaid() {
		if _, err := h.PaymentReceiver.ConfirmByExternalRef(r.Context(), ev.UUID); err != nil {
			observability.GatewayCallbacksTotal.WithLabelValues("heleket", "confirm_failed").Inc()
			logger.Error("ConfirmByExternalRef failed", "error", err.Error())
		} else {
			observability.GatewayCallbacksTotal.WithLabelValues("heleket", "confirmed").Inc()
		}
	} else {
		observability.GatewayCallbacksTotal.WithLabelValues("heleket", "ignored").Inc()
	}
	w.WriteHeader(http.StatusOK)
}

// --- Admin: users + credits + orders --- //

func (h *Handlers) AdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.Users.ListWithCreditBalance(r.Context(), 200)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, users)
}

func (h *Handlers) AdminGetUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u, err := h.Users.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	acct, _ := h.Credits.Balance(r.Context(), id)
	txs, _ := h.Credits.History(r.Context(), id, 100)
	profs, _ := h.Profiles.List(r.Context(), id)
	writeData(w, http.StatusOK, map[string]any{
		"user":         u,
		"credits":      acct,
		"transactions": txs,
		"profiles":     profs,
	})
}

func (h *Handlers) AdminAdjustCredits(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		DeltaCents  int64  `json:"delta_cents"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if body.Description == "" {
		body.Description = "Ajuste manual"
	}
	acct, err := h.Credits.AdminAdjustment(r.Context(), id, body.DeltaCents, body.Description)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, acct)
}

func (h *Handlers) AdminMarkOrderPaid(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.PaymentReceiver.MarkOrderPaid(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResendWebhook recebe eventos da Resend (delivered/bounced/complained).
// Verificação de assinatura via header `svix-signature` (Resend usa Svix).
// Por enquanto sem signature check — Resend Webhook Signing Key fica como
// follow-up; HML é aceitável receber sem auth (endpoint não é mutativo
// pra estado crítico, só atualiza reputation).
//
// Resposta 200 sempre que body parseia. Resend re-tenta em 5xx.
func (h *Handlers) ResendWebhook(w http.ResponseWriter, r *http.Request) {
	logger := observability.FromContext(r.Context()).With("provider", "resend")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	// Svix signature check (Fase 4.4 follow-up). Lemos o secret direto do
	// env pra evitar engordar a struct Handlers — endpoint singleton, custo
	// desprezível por request. Vazio = skip (HML/dev).
	if secret := os.Getenv("RESEND_WEBHOOK_SECRET"); secret != "" {
		svixID := r.Header.Get("svix-id")
		svixTS := r.Header.Get("svix-timestamp")
		svixSig := r.Header.Get("svix-signature")
		if err := email.VerifySvixSignature(body, svixID, svixTS, svixSig, secret); err != nil {
			logger.Warn("svix signature invalid", "error", err.Error())
			writeError(w, domain.ErrUnauthorized)
			return
		}
	}
	if h.EmailRepu == nil {
		// Service não wireado — só registramos e seguimos.
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := h.EmailRepu.RecordResendEvent(r.Context(), body); err != nil {
		logger.Warn("record resend event failed", "error", err.Error())
		writeError(w, domain.ErrInvalidInput)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// PublicValidateCoupon — preview do desconto sem comprometer used_count.
// Front chama isso pra mostrar "$X off com BLACK10" antes do submit.
func (h *Handlers) PublicValidateCoupon(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code            string `json:"code"`
		PlanID          string `json:"plan_id"`
		Email           string `json:"email"`
		DisplayCurrency string `json:"display_currency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if h.Coupons == nil || h.Plans == nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	plan, err := h.Plans.GetByID(r.Context(), body.PlanID)
	if err != nil {
		writeError(w, err)
		return
	}
	preview, err := h.Coupons.Preview(r.Context(), application.PreviewInput{
		Code:           body.Code,
		AmountUSDCents: plan.PriceCents,
		PlanCategory:   plan.Category,
		UserEmail:      body.Email,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, preview)
}

// Admin CRUD ----

func (h *Handlers) AdminListCoupons(w http.ResponseWriter, r *http.Request) {
	if h.Coupons == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	list, err := h.Coupons.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

func (h *Handlers) AdminCreateCoupon(w http.ResponseWriter, r *http.Request) {
	var c domain.Coupon
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	out, err := h.Coupons.Create(r.Context(), c)
	if err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "create", "coupon", out.Code, nil, out)
	writeData(w, http.StatusCreated, out)
}

func (h *Handlers) AdminUpdateCoupon(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	var c domain.Coupon
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	c.Code = code
	before, _ := h.Coupons.Get(r.Context(), code)
	out, err := h.Coupons.Update(r.Context(), c)
	if err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "update", "coupon", code, before, out)
	writeData(w, http.StatusOK, out)
}

func Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// PublicStatus devolve o snapshot do estado dos componentes principais —
// consumido pela página /status do storefront. Cada serviço tem um nome
// curto (mostrado no card) e um status: "operational" | "degraded" | "down".
//
// "degraded" significa que o serviço respondeu mas com indicador anormal
// (ex.: drift > 0 em plan_prices, latência DB > 200ms). "down" é falha
// total. O HTTP status fica sempre 200 — quem consome decide o que mostrar.
func (h *Handlers) PublicStatus(w http.ResponseWriter, r *http.Request) {
	type service struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		Detail    string `json:"detail,omitempty"`
		LatencyMs int64  `json:"latency_ms,omitempty"`
	}
	type payload struct {
		Timestamp string    `json:"timestamp"`
		Overall   string    `json:"overall"`
		Services  []service `json:"services"`
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	out := payload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Services:  make([]service, 0, 4),
	}

	out.Services = append(out.Services, service{Name: "API", Status: "operational"})

	// Database
	dbStart := time.Now()
	dbStatus := "operational"
	dbDetail := ""
	if h.DB != nil {
		if err := h.DB.Pool().Ping(ctx); err != nil {
			dbStatus = "down"
			dbDetail = "ping failed"
		} else if elapsed := time.Since(dbStart); elapsed > 200*time.Millisecond {
			dbStatus = "degraded"
			dbDetail = "slow ping"
		}
	}
	out.Services = append(out.Services, service{
		Name: "Database", Status: dbStatus, Detail: dbDetail,
		LatencyMs: time.Since(dbStart).Milliseconds(),
	})

	// Plan prices invariant — total de drift atual.
	driftStatus := "operational"
	driftDetail := ""
	if h.DB != nil {
		var total int64
		err := h.DB.Pool().QueryRow(ctx, `
			SELECT COUNT(*) FROM plan_prices pp
			JOIN plans p ON p.id=pp.plan_id
			JOIN currencies c ON c.code=pp.currency_code
			WHERE pp.amount::numeric IS DISTINCT FROM
			      ROUND((p.price_cents::numeric / 100.0) * c.rate::numeric, c.decimals)
			  AND pp.amount ~ '^[0-9]+(\.[0-9]+)?$'`).Scan(&total)
		if err != nil {
			driftStatus = "degraded"
			driftDetail = "drift check failed"
		} else if total > 0 {
			driftStatus = "degraded"
			driftDetail = "stale rows in plan_prices"
		}
	}
	out.Services = append(out.Services, service{
		Name: "Plan prices", Status: driftStatus, Detail: driftDetail,
	})

	// Overall = pior status entre os serviços.
	out.Overall = "operational"
	for _, s := range out.Services {
		if s.Status == "down" {
			out.Overall = "down"
			break
		}
		if s.Status == "degraded" {
			out.Overall = "degraded"
		}
	}

	writeData(w, http.StatusOK, out)
}

// ReadyHandler devolve um http.Handler que executa `check` (tipicamente db.Ping).
// 200 quando check==nil ou check() retorna nil; 503 caso contrário.
// O response não vaza o erro pra fora — só status e mensagem genérica. O erro
// completo vai pro log estruturado.
func ReadyHandler(check ReadyChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			if err := check(r); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"status": "unavailable",
					"reason": "dependency check failed",
				})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
}

// MeGetOrder devolve o pedido completo do user logado pra renderizar a
// página de tracking (/account/orders/{id}). Autorização concentrada no
// OrderService — handler só extrai userID + id e delega. ErrNotFound
// quando o pedido não existe OU pertence a outro user, sem distinção.
func (h *Handlers) MeGetOrder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	o, err := h.OrderSvc.GetByIDForUser(r.Context(), userID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, o)
}

// PublicCountryPPP devolve o catálogo de multipliers PPP (Fase 6.5). Front
// baixa uma vez por sessão e aplica via priceForCountry() — display_amount
// adaptado ao poder de compra local. USD canonical / settlement intocados.
//
// Lê via h.DB direto pra não exigir nova dep na struct Handlers (main loop
// pluga CountryPPPRepository depois). Países ausentes equivalem a 1.00 — o
// front trata. Pequeno (<50 linhas) → sem paginação.
func (h *Handlers) PublicCountryPPP(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		writeData(w, http.StatusOK, []domain.CountryPPP{})
		return
	}
	rows, err := h.DB.Pool().Query(r.Context(),
		`SELECT country_code, multiplier FROM country_ppp ORDER BY country_code`,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	out := []domain.CountryPPP{}
	for rows.Next() {
		var p domain.CountryPPP
		if err := rows.Scan(&p.Code, &p.Multiplier); err != nil {
			writeError(w, err)
			return
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, out)
}

// MeGetNotifPrefs — GET /v1/me/notif-prefs
// Devolve as 4 chaves canônicas (order_updates, marketing, reviews,
// cart_recovery) com defaults aplicados quando ausentes. Front usa pra
// renderizar os toggles em /account/notifications.
func (h *Handlers) MeGetNotifPrefs(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.Notifs == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	prefs, err := h.Notifs.GetPrefs(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, prefs)
}

// MeUpdateNotifPrefs — PUT /v1/me/notif-prefs
// Body: { order_updates?: bool, marketing?: bool, reviews?: bool, cart_recovery?: bool }
// Merge no JSONB: chaves ausentes são preservadas; chaves fora da allowlist
// devolvem 400. Idempotente.
func (h *Handlers) MeUpdateNotifPrefs(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.Notifs == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	var body map[string]bool
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Notifs.UpdatePrefs(r.Context(), userID, body); err != nil {
		writeError(w, err)
		return
	}
	prefs, err := h.Notifs.GetPrefs(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, prefs)
}

// --- Manage my data (LGPD/GDPR — Fase 5.2) --- //

// MeExportData devolve um JSON com tudo que o sistema sabe do usuário
// (orders, tickets, profiles, reviews, prefs). Force-download via
// Content-Disposition pra UX clara: o usuário clica e o browser salva
// `viralefy-data.json` direto.
func (h *Handlers) MeExportData(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.UserData == nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	data, err := h.UserData.ExportData(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=viralefy-data.json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(data)
}

// MeRequestDeletion agenda exclusão da conta. Body opcional: {"reason"}.
// 30 dias de janela pra cancelar antes do hard-delete físico (cron futuro,
// tech debt).
func (h *Handlers) MeRequestDeletion(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.UserData == nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	// Body pode vir vazio — sem reason é OK.
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := h.UserData.RequestDeletion(r.Context(), userID, body.Reason); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// MeCancelDeletion desfaz um pedido pendente. Idempotente — chamar sem
// request ativa é no-op (204).
func (h *Handlers) MeCancelDeletion(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.UserData == nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.UserData.CancelDeletion(r.Context(), userID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MeGetDeletion devolve o estado corrente do pedido de exclusão (se
// houver) + listas de categorias deletadas vs retidas pra transparência
// LGPD Art. 9. Retorna 200 sempre — status="none" quando o usuário
// nunca pediu.
func (h *Handlers) MeGetDeletion(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.UserData == nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	st, err := h.UserData.GetDeletionStatus(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// PublicJWKS expõe a chave pública RSA (Fase 4.1) em
// /.well-known/jwks.json — consumidores externos (verificadores
// stateless) podem validar tokens RS256 sem chamar a API. Lê a chave
// privada que já vive dentro do AuthService pra evitar carregar
// /etc/viralefy/jwt-rs256.pem duas vezes.
func (h *Handlers) PublicJWKS(w http.ResponseWriter, r *http.Request) {
	if h.Auth == nil || h.Auth.RSAPrivKey == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	jwks, err := jwtkeys.PublicJWKS(h.Auth.RSAPrivKey)
	if err != nil {
		writeError(w, err)
		return
	}
	// Cache curto (5 min) — clientes podem cachear mais tempo via Cache-Control
	// quando rotação for implementada com janela de overlap.
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, jwks)
}

// --- A/B testing harness (Fase 6.6) --- //
//
// Endpoints públicos:
//   POST /v1/ab/assign — devolve variant pra um (visitor_id, experiment_key).
//   POST /v1/ab/track  — registra evento ("exposure"|"conversion"|custom).
//
// Admin (RBAC: admins:manage):
//   GET  /v1/admin/ab/experiments
//   POST /v1/admin/ab/experiments
//   PUT  /v1/admin/ab/experiments/{key}
//
// Visitor ID vem do front (UUID em cookie/localStorage 1y). Sticky
// assignment garante reprodutibilidade entre dispositivos do mesmo visitor.

// PublicABAssign — atribui (ou recupera) a variant do visitor.
// Body: { visitor_id, experiment_key }
// Resp: { variant }
//
// Quando o experimento está inativo, devolve { variant: "control" } como
// fallback seguro — o front renderiza a variant default sem quebrar.
// Quando o experimento não existe, devolve 404.
func (h *Handlers) PublicABAssign(w http.ResponseWriter, r *http.Request) {
	if h.ABTests == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	var body struct {
		VisitorID     string `json:"visitor_id"`
		ExperimentKey string `json:"experiment_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	variant, err := h.ABTests.GetAssignment(r.Context(), body.VisitorID, body.ExperimentKey)
	if err != nil {
		// Inativo → fallback graceful pra "control" sem 4xx.
		if err == domain.ErrExperimentInactive {
			writeData(w, http.StatusOK, map[string]string{"variant": "control"})
			return
		}
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"variant": variant})
}

// PublicABTrack — registra um evento.
// Body: { visitor_id, experiment_key, event_name, payload? }
func (h *Handlers) PublicABTrack(w http.ResponseWriter, r *http.Request) {
	if h.ABTests == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	var body struct {
		VisitorID     string         `json:"visitor_id"`
		ExperimentKey string         `json:"experiment_key"`
		EventName     string         `json:"event_name"`
		Payload       map[string]any `json:"payload,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.ABTests.TrackEvent(r.Context(), body.VisitorID, body.ExperimentKey, body.EventName, body.Payload); err != nil {
		// Inativo: silenciar (204) — evento não conta mas não é erro pro
		// cliente. Outros erros propagam.
		if err == domain.ErrExperimentInactive {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminListAB — lista todos os experimentos pro backoffice.
func (h *Handlers) AdminListAB(w http.ResponseWriter, r *http.Request) {
	if h.ABTests == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	list, err := h.ABTests.AdminListExperiments(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// AdminCreateAB — cria experimento.
// Body: { key, description, variants: {variant: weight}, active }
func (h *Handlers) AdminCreateAB(w http.ResponseWriter, r *http.Request) {
	if h.ABTests == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	var e domain.ABExperiment
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	out, err := h.ABTests.AdminCreateExperiment(r.Context(), e)
	if err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "create", "ab_experiment", out.Key, nil, out)
	writeData(w, http.StatusCreated, out)
}

// AdminUpdateAB — atualiza descrição, pesos e flag active. Key imutável.
func (h *Handlers) AdminUpdateAB(w http.ResponseWriter, r *http.Request) {
	if h.ABTests == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	key := chi.URLParam(r, "key")
	var e domain.ABExperiment
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	e.Key = key
	out, err := h.ABTests.AdminUpdateExperiment(r.Context(), e)
	if err != nil {
		writeError(w, err)
		return
	}
	h.logAudit(r, "update", "ab_experiment", key, nil, out)
	writeData(w, http.StatusOK, out)
}

// --- Referrals (Fase 6.4) --- //

// MeGetMyReferral devolve {code, total_referred, total_earned_cents}
// para o painel /account/referral. EnsureCode roda on-demand: usuários
// que nunca acessaram a aba ainda assim ganham código aqui.
func (h *Handlers) MeGetMyReferral(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.Referrals == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	stats, err := h.Referrals.MyStats(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, stats)
}

// PublicReferralInfo é o endpoint anônimo consumido pelo checkout pra
// renderizar o selo "Convidado por X" (primeiro nome apenas). Resposta
// sempre 200 — quando o código não existe, devolve {valid:false} pro
// front degradar silenciosamente sem 404 ruidoso no console.
func (h *Handlers) PublicReferralInfo(w http.ResponseWriter, r *http.Request) {
	if h.Referrals == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	code := chi.URLParam(r, "code")
	info, err := h.Referrals.PublicInfo(r.Context(), code)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, info)
}

// --- Anti-fraude (Fase 4.3) --- //

// AdminListFraudSignals devolve a timeline de sinais gravados pelo
// FraudVelocityCron + checagens inline. Filtros opcionais por actor
// (email/IP substring) e severity (warn|block). Limite default 100.
func (h *Handlers) AdminListFraudSignals(w http.ResponseWriter, r *http.Request) {
	if h.Fraud == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	actor := strings.TrimSpace(r.URL.Query().Get("actor"))
	severity := strings.TrimSpace(r.URL.Query().Get("severity"))
	if severity != "" && severity != "warn" && severity != "block" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			limit = n
		}
	}
	list, err := h.Fraud.ListSignals(r.Context(), actor, severity, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// PublicTaxRates devolve o catálogo de alíquotas fiscais (Fase 5.3 — VAT
// UE+GB). Front baixa uma vez por sessão e pre-computa a linha de VAT no
// checkout antes do submit. A autoridade do cálculo final é o
// TaxService.ComputeTax server-side; este endpoint serve só pra display.
//
// Tabela pequena (<40 linhas) → sem paginação. Países ausentes equivalem
// a rate 0% e o front trata. Cache-Control fica como o resto dos catálogos
// públicos (CDN/edge caching definido fora daqui).
func (h *Handlers) PublicTaxRates(w http.ResponseWriter, r *http.Request) {
	if h.TaxRates == nil {
		writeData(w, http.StatusOK, []domain.TaxRate{})
		return
	}
	list, err := h.TaxRates.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// --- Subscriptions (Fase 6.3) ---
//
// Subscriptions são planos mensais recorrentes. O cron de renovação
// (SubscriptionCron) gera uma order pending a cada ciclo via
// CheckoutService.Checkout, e o user paga via payment_url normal.
// 3 falhas seguidas → cancela auto.

// MeListMySubscriptions devolve subs do user autenticado (active +
// cancelled), ordenadas por created_at DESC.
func (h *Handlers) MeListMySubscriptions(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.Subscriptions == nil {
		writeData(w, http.StatusOK, []domain.Subscription{})
		return
	}
	subs, err := h.Subscriptions.ListByUser(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, subs)
}

// MeSubscribe cria sub ativa. Body: {plan_id}. Idempotente (já existir
// active → devolve a mesma). NÃO gera o primeiro pagamento; o user
// continua precisando fazer um checkout manual pro ciclo 0.
func (h *Handlers) MeSubscribe(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.Subscriptions == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	var body struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	sub, err := h.Subscriptions.Subscribe(r.Context(), userID, body.PlanID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusCreated, sub)
}

// MeCancelSubscription cancela a sub do user (valida ownership no
// service). DELETE em /v1/me/subscriptions/{id}.
func (h *Handlers) MeCancelSubscription(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.Subscriptions == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Subscriptions.Cancel(r.Context(), id, userID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- User behavior tracking (Wave 5) --- //
//
// /v1/track é público (sem auth) — visitor_id é client-supplied via JS no
// browser. Rate-limited via mutationLimiter pra mitigar abuso. Eventos são
// granulares (pageview/click/modal_*/checkout_*/abandon/landing) e populam
// user_events + bumpam user_journeys quando há sessão. Best-effort: erros
// internos viram warn e devolvem 204 (não quebra UX).
//
// /v1/me/journey é a leitura autenticada do agregado + últimos 50 eventos
// do user logado (usado pelo backoffice/account pra ver atribuição).

// PublicTrackEvent — captura evento behavioral.
// Body: { visitor_id, event_type, path?, referrer?, payload?, utm? }.
// event_type whitelist: pageview | click | modal_open | modal_close |
//                        checkout_start | checkout_complete | abandon | landing.
// Quando há JWT user na request, popula user_id automaticamente (cross-
// correlate anônimo→autenticado).
func (h *Handlers) PublicTrackEvent(w http.ResponseWriter, r *http.Request) {
	if h.Events == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	// Cap body em 1MB. Endpoint público sem auth — payload/utm map[string]any
	// poderia receber 100MB JSON e esgotar memória. 1MB cobre largest legítimo
	// (batch de 10 eventos com payload moderado) com folga.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		VisitorID string         `json:"visitor_id"`
		EventType string         `json:"event_type"`
		Path      string         `json:"path"`
		Referrer  string         `json:"referrer"`
		Payload   map[string]any `json:"payload,omitempty"`
		UTM       map[string]any `json:"utm,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if body.VisitorID == "" || body.EventType == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if !application.IsAllowedEventType(body.EventType) {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	// user_id é opcional — só populado quando o request veio com JWT user.
	uid := userIDFromContext(r.Context())
	// LGPD Art. 8 §3: IP/UA só vão pro DB se o front enviar
	// X-Analytics-Consent: 1 (consent explícito). "0" ou ausente → repo
	// NULLifica os dois campos. Mantemos o flag (true/false) na própria
	// row pra auditoria.
	consentVal := readAnalyticsConsentHeader(r)
	in := application.EventInput{
		VisitorID:        body.VisitorID,
		UserID:           uid,
		EventType:        body.EventType,
		Path:             body.Path,
		Referrer:         body.Referrer,
		Payload:          body.Payload,
		UTM:              body.UTM,
		IP:               clientIP(r),
		UserAgent:        r.UserAgent(),
		AnalyticsConsent: consentVal,
	}
	// RecordEvent é best-effort — não propaga erros (a não ser validação).
	if err := h.Events.RecordEvent(r.Context(), in); err != nil {
		// ErrInvalidInput vem da validação no service (defesa em
		// profundidade). Devolve 400 nesse caso; outros erros já viraram
		// warn no logger e o service retorna nil.
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MeJourney — devolve o agregado do user logado + os últimos 50 eventos.
// Resposta: { journey: UserJourney, events: []UserEvent }.
func (h *Handlers) MeJourney(w http.ResponseWriter, r *http.Request) {
	if h.Events == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	journey, err := h.Events.GetJourney(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	events, err := h.Events.ListByUser(r.Context(), userID, 50)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"journey": journey,
		"events":  events,
	})
}

// ---------- Bulk soft delete ------------------------------------------------
//
// 3 endpoints (orders/invoices/users), todos gated por PermAdminsManage.
// Body: { ids: ["..."], reason?: "..." }. Limite de 200 ids por chamada
// pra evitar SQL flood. Erro por id é loggado mas não interrompe — devolve
// resumo final {succeeded, failed: [{id, error}]}.
//
// Hard delete em massa NÃO existe pra não dar arma de destruição em massa
// nem pra superadmin. Pra purgar precisa ir 1 por 1 na aba Trash.

type bulkDeleteRequest struct {
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
}

type bulkDeleteResponse struct {
	Succeeded int                  `json:"succeeded"`
	Failed    []bulkDeleteFailure  `json:"failed,omitempty"`
}

type bulkDeleteFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

func decodeBulkDelete(r *http.Request) (*bulkDeleteRequest, error) {
	var b bulkDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		return nil, err
	}
	if len(b.IDs) == 0 {
		return nil, domain.ErrInvalidInput
	}
	if len(b.IDs) > 200 {
		return nil, domain.ErrInvalidInput
	}
	return &b, nil
}

func (h *Handlers) AdminBulkSoftDeleteOrders(w http.ResponseWriter, r *http.Request) {
	body, err := decodeBulkDelete(r)
	if err != nil {
		writeError(w, err)
		return
	}
	caller, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	resp := bulkDeleteResponse{}
	for _, id := range body.IDs {
		if err := h.Orders.SoftDeleteOrder(r.Context(), id, caller.AdminID, body.Reason); err != nil {
			resp.Failed = append(resp.Failed, bulkDeleteFailure{ID: id, Error: err.Error()})
			continue
		}
		resp.Succeeded++
	}
	writeData(w, http.StatusOK, resp)
}

func (h *Handlers) AdminBulkSoftDeleteInvoices(w http.ResponseWriter, r *http.Request) {
	body, err := decodeBulkDelete(r)
	if err != nil {
		writeError(w, err)
		return
	}
	caller, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	resp := bulkDeleteResponse{}
	for _, id := range body.IDs {
		if err := h.Invoices.AdminSoftDelete(r.Context(), id, caller.AdminID, body.Reason); err != nil {
			resp.Failed = append(resp.Failed, bulkDeleteFailure{ID: id, Error: err.Error()})
			continue
		}
		resp.Succeeded++
	}
	writeData(w, http.StatusOK, resp)
}

func (h *Handlers) AdminBulkSoftDeleteUsers(w http.ResponseWriter, r *http.Request) {
	body, err := decodeBulkDelete(r)
	if err != nil {
		writeError(w, err)
		return
	}
	caller, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	resp := bulkDeleteResponse{}
	for _, id := range body.IDs {
		if err := h.Users.SoftDeleteUser(r.Context(), id, caller.AdminID, body.Reason); err != nil {
			resp.Failed = append(resp.Failed, bulkDeleteFailure{ID: id, Error: err.Error()})
			continue
		}
		resp.Succeeded++
	}
	writeData(w, http.StatusOK, resp)
}

// AdminTrash — GET /v1/admin/trash (superadmin only)
//
// Endpoint consolidado pra aba "Trash" do painel admin. Devolve TODOS os
// items soft-deleted das 3 entidades principais:
//
//   - orders   (até 100 mais recentes deletados, hidratado com plan+user)
//   - invoices (idem, com user)
//   - users    (idem, com saldo de credit_accounts)
//
// Cada item carrega deleted_at + deleted_by_admin_id + delete_reason pra
// trilha. UI lista, deixa o superadmin clicar pra ir no detail page de cada
// um e restaurar ou hard-delete.
//
// Por que consolidado em vez de 3 endpoints separados:
// o caso de uso é "ver tudo que admin apagou de uma vez" — 1 round-trip
// é mais barato. Volume é baixo (deletes são raros), max 300 items total.
func (h *Handlers) AdminTrash(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 && n <= 500 {
			limit = n
		}
	}
	orders, err := h.Orders.ListDeletedView(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	invoices, err := h.Invoices.AdminListDeleted(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	users, err := h.Users.ListDeletedWithCreditBalance(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"orders":   orders,
		"invoices": invoices,
		"users":    users,
	})
}

// ---------- Admin SOFT / HARD delete ---------------------------------------
//
// 3 entidades cobertas: orders, invoices, users. Cada uma tem 3 rotas:
//
//   DELETE /v1/admin/<entity>/{id}            soft  (PermAdminsManage)
//   DELETE /v1/admin/<entity>/{id}/hard       hard  (RequireSuperadmin)
//   POST   /v1/admin/<entity>/{id}/restore    restaura (RequireSuperadmin)
//
// Body opcional pra soft delete (JSON): {"reason": "fraud / refunded / etc"}
// O campo deleted_by_admin_id é gravado a partir do principal do request.
//
// Convenção de resposta: 204 No Content em sucesso (sem payload). Erros
// canônicos via writeError (404 NOT_FOUND quando id inexistente).

type deleteRequestBody struct {
	Reason string `json:"reason"`
}

func decodeDeleteBody(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	var b deleteRequestBody
	_ = json.NewDecoder(r.Body).Decode(&b)
	return strings.TrimSpace(b.Reason)
}

// AdminSoftDeleteOrder — DELETE /v1/admin/orders/{id}
func (h *Handlers) AdminSoftDeleteOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	reason := decodeDeleteBody(r)
	if err := h.Orders.SoftDeleteOrder(r.Context(), id, p.AdminID, reason); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminHardDeleteOrder — DELETE /v1/admin/orders/{id}/hard (superadmin)
func (h *Handlers) AdminHardDeleteOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Orders.HardDeleteOrder(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminRestoreOrder — POST /v1/admin/orders/{id}/restore (superadmin)
func (h *Handlers) AdminRestoreOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Orders.RestoreOrder(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminSoftDeleteInvoice — DELETE /v1/admin/invoices/{id}
func (h *Handlers) AdminSoftDeleteInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	reason := decodeDeleteBody(r)
	if err := h.Invoices.AdminSoftDelete(r.Context(), id, p.AdminID, reason); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) AdminHardDeleteInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Invoices.AdminHardDelete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) AdminRestoreInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Invoices.AdminRestore(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminSoftDeleteUser — DELETE /v1/admin/users/{id}
func (h *Handlers) AdminSoftDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	reason := decodeDeleteBody(r)
	if err := h.Users.SoftDeleteUser(r.Context(), id, p.AdminID, reason); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) AdminHardDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Users.HardDeleteUser(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) AdminRestoreUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Users.RestoreUser(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminUserJourney — GET /v1/admin/users/{id}/journey
// Espelha MeJourney mas pra qualquer user lookup-by-id. Devolve journey
// agregado + últimos 100 eventos (mais alto que MeJourney pra dar contexto
// completo pro admin investigar).
func (h *Handlers) AdminUserJourney(w http.ResponseWriter, r *http.Request) {
	if h.Events == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	userID := chi.URLParam(r, "id")
	if userID == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	journey, err := h.Events.GetJourney(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	events, err := h.Events.ListByUser(r.Context(), userID, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"journey": journey,
		"events":  events,
	})
}

// AdminListVisitors — GET /v1/admin/visitors?limit=&offset=
// Lista paginada de visitors agrupados (anônimos + convertidos). Usado pelo
// painel admin `/analytics/visitors`.
func (h *Handlers) AdminListVisitors(w http.ResponseWriter, r *http.Request) {
	if h.Events == nil {
		writeData(w, http.StatusOK, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	out, err := h.Events.ListRecentVisitors(r.Context(), limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, out)
}

// AdminGetVisitor — GET /v1/admin/visitors/{vid}
// Devolve o agregado do visitor + últimos 100 eventos.
func (h *Handlers) AdminGetVisitor(w http.ResponseWriter, r *http.Request) {
	if h.Events == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	vid := chi.URLParam(r, "vid")
	if vid == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	summary, err := h.Events.GetVisitorSummary(r.Context(), vid)
	if err != nil {
		writeError(w, err)
		return
	}
	events, err := h.Events.ListByVisitor(r.Context(), vid, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"summary": summary,
		"events":  events,
	})
}

// readAnalyticsConsentHeader — lê o header X-Analytics-Consent ("1" / "0").
// Retorna *bool: nil quando o header é ausente/inválido (caller decide o
// que fazer — o repo é conservador e NULLifica IP/UA).
//
// LGPD Art. 8 §3: o front DEVE mandar "1" só quando o usuário aceitou
// analytics. Ausência do header = trate como negação (privacy-by-default).
func readAnalyticsConsentHeader(r *http.Request) *bool {
	v := strings.TrimSpace(r.Header.Get("X-Analytics-Consent"))
	if v == "" {
		f := false
		return &f
	}
	switch v {
	case "1", "true", "TRUE":
		t := true
		return &t
	case "0", "false", "FALSE":
		f := false
		return &f
	default:
		// Valor estranho — log warn (mas só num lugar com contexto) e
		// trate como denied.
		f := false
		return &f
	}
}

// PublicRecordConsent — POST /v1/me/consent
//
// Append-only audit log da decisão de consent de cookies. Aceita
// anônimo (sem JWT — visitor_id opcional no body) ou autenticado
// (user_id derivado do JWT). IP+UA são sempre gravados aqui porque a
// base legal é a própria comprovação do consentimento (Art. 8 §6 LGPD).
//
// Body:
//   {
//     version: number,
//     necessary: boolean,
//     preferences: boolean,
//     analytics: boolean,
//     marketing: boolean,
//     timestamp: string (ISO 8601),
//     source: "accept_all" | "essential_only" | "custom" | "reset",
//     visitor_id?: string
//   }
//
// Best-effort: erros internos viram 204 (não quebra UX); validação
// retorna 400.
func (h *Handlers) PublicRecordConsent(w http.ResponseWriter, r *http.Request) {
	if h.Consent == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10) // 4KB — payload é pequeno
	var body struct {
		Version     int    `json:"version"`
		Necessary   bool   `json:"necessary"`
		Preferences bool   `json:"preferences"`
		Analytics   bool   `json:"analytics"`
		Marketing   bool   `json:"marketing"`
		Timestamp   string `json:"timestamp"`
		Source      string `json:"source"`
		VisitorID   string `json:"visitor_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	uid := userIDFromContext(r.Context())
	ts := time.Time{}
	if body.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, body.Timestamp); err == nil {
			ts = parsed
		}
	}
	in := application.ConsentInput{
		UserID:      uid,
		VisitorID:   body.VisitorID,
		Version:     body.Version,
		Necessary:   body.Necessary,
		Preferences: body.Preferences,
		Analytics:   body.Analytics,
		Marketing:   body.Marketing,
		Source:      body.Source,
		IP:          clientIP(r),
		UserAgent:   r.UserAgent(),
		Timestamp:   ts,
	}
	if err := h.Consent.Record(r.Context(), in); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PublicListPaymentMethods — GET /v1/plans/{id}/payment-methods
// Catálogo de métodos de pagamento disponíveis para um plano específico,
// com preview de quanto o cliente paga em CADA método (já convertido pra
// moeda nativa do gateway). UI usa pra montar a lista de cards no checkout.
//
// Query params:
//   display_currency — preferida do user (BRL/USD/EUR/USDT). Default USD.
//   country          — código ISO alpha-2 minúsculo do comprador (futuro
//                      filtro por região; hoje só passa por).
func (h *Handlers) PublicListPaymentMethods(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "id")
	if planID == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	q := r.URL.Query()
	// PHASE-8 Wave 3: quando o microserviço viralefy_payments está plugado
	// (MethodsRemote != nil), proxy direto via HTTP. Shape de saída idêntica
	// — o paymentsclient devolve o mesmo PaymentMethodOption serializado.
	// Modo legado (MethodsRemote==nil): cai no Checkout.ListPaymentMethods
	// in-memory.
	if h.MethodsRemote != nil {
		methods, err := h.MethodsRemote.ListMethods(
			r.Context(), planID,
			q.Get("display_currency"),
			q.Get("country"),
		)
		if err != nil {
			// Plan UUID inexistente: payments microservice retorna 404,
			// paymentsclient wrappeia em ErrNotFound. Traduzimos pra
			// domain.ErrNotFound aqui pro writeError emitir 404 no client
			// final (era 500 generico — round 20 simulated bug).
			if errors.Is(err, paymentsclient.ErrNotFound) {
				writeError(w, domain.ErrNotFound)
				return
			}
			writeError(w, err)
			return
		}
		writeData(w, http.StatusOK, methods)
		return
	}
	methods, err := h.Checkout.ListPaymentMethods(
		r.Context(), planID,
		q.Get("display_currency"),
		q.Get("country"),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, methods)
}

// MeUploadProof — POST /v1/me/orders/{id}/proof
// Cliente anexa comprovante de pagamento (PIX, crypto on-chain).
//
// Dois content-types suportados:
//   - multipart/form-data — preferido. Backend faz PutObject no MinIO/R2 e
//     guarda só a key em orders.proof_url. Limite 5MB. MIME whitelist.
//   - application/json (legacy) — body {"file_url": data:URL ou http URL,
//     "file_name", "mime_type", "size_bytes", "note"}. Usado quando storage
//     está disabled (NoopStorage) ou pra hosts terceiros (imgur etc).
//
// Sucesso: 200 com a Order atualizada (proof_status=pending). Admin revisa
// em /backoffice/orders/{id} e clica "Approve" pra disparar mark-as-paid.
//
// Tamanho max:
//   - multipart: 5MB
//   - JSON: 1MB (limite global de MaxBytesReader)
func (h *Handlers) MeUploadProof(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	orderID := chi.URLParam(r, "id")
	if orderID == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	o, err := h.OrderSvc.GetByIDForUser(r.Context(), userID, orderID)
	if err != nil {
		writeError(w, err)
		return
	}
	if o.Status != domain.OrderStatusPending {
		writeError(w, domain.ErrInvalidInput)
		return
	}

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		h.uploadProofMultipart(w, r, userID, orderID)
		return
	}
	// Fluxo legacy JSON: mantém pra hosts terceiros (imgur) ou storage off.
	var body struct {
		FileURL   string `json:"file_url"`
		FileName  string `json:"file_name,omitempty"`
		MimeType  string `json:"mime_type,omitempty"`
		SizeBytes int    `json:"size_bytes,omitempty"`
		Note      string `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	body.FileURL = strings.TrimSpace(body.FileURL)
	if body.FileURL == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	// Fluxo legacy JSON: storageKey="" → proof_storage_key fica NULL e o
	// reader cai em proof_url (data:/http) como antes. Quando esse handler
	// é usado, NÃO subimos pro MinIO (cliente já deu URL externa ou base64).
	if err := h.Orders.SetProof(
		r.Context(), orderID, body.FileURL, body.FileName, body.MimeType, body.Note, body.SizeBytes, "",
	); err != nil {
		writeError(w, err)
		return
	}
	updated, err := h.OrderSvc.GetByIDForUser(r.Context(), userID, orderID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, updated)
}

// AdminProofDecision — POST /v1/admin/orders/{id}/proof/decision
// Admin revisa o comprovante anexado pelo cliente e marca approved ou
// rejected. approved dispara mark-as-paid em sequência (fecha o loop
// PIX/USDT manual: cliente paga → anexa → admin aprova → order ativada).
// rejected mantém em pending com nota do reviewer; cliente reanexa.
func (h *Handlers) AdminProofDecision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Decision string `json:"decision"` // "approved" | "rejected"
		Note     string `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	body.Decision = strings.ToLower(strings.TrimSpace(body.Decision))
	if body.Decision != "approved" && body.Decision != "rejected" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	o, err := h.Orders.GetByID(r.Context(), id)
	if err != nil || o == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	if o.ProofURL == nil || *o.ProofURL == "" {
		// Sem comprovante anexado — admin clicou approve em order sem proof?
		// Bloqueamos pra evitar mark-as-paid acidental por click errado.
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.Orders.SetProofStatus(r.Context(), id, body.Decision, body.Note); err != nil {
		writeError(w, err)
		return
	}
	if body.Decision == "approved" {
		// Dispara o pipeline normal pós-pagamento (ticket aberto, email,
		// delivery capture). Idempotente — pode chamar 2x sem efeito duplo.
		if err := h.PaymentReceiver.MarkOrderPaid(r.Context(), id); err != nil {
			writeError(w, err)
			return
		}
	} else if h.Email != nil {
		// Rejected: cliente precisa saber que precisa reanexar. PaymentReceiver
		// NÃO é chamado, então o email fica conosco aqui. Best-effort: erro
		// no envio loga e segue (decisão de admin já foi gravada).
		if user, err := h.Users.GetByID(r.Context(), o.UserID); err == nil && user != nil {
			_ = h.Email.Send(r.Context(), buildProofRejectionEmail(user.Name, user.Email, id, body.Note))
		}
	}
	updated, _ := h.Orders.GetByID(r.Context(), id)
	writeData(w, http.StatusOK, updated)
}

// buildProofRejectionEmail compõe a mensagem enviada ao cliente quando
// admin rejeita o comprovante. Texto curto, focado na ação: "anexa de
// novo". Reason é opcional — admin pode deixar vazio.
func buildProofRejectionEmail(name, to, orderID, reason string) application.EmailMessage {
	short := orderID
	if len(short) > 8 {
		short = short[:8]
	}
	reasonBlock := ""
	if strings.TrimSpace(reason) != "" {
		reasonBlock = "<p><strong>Reviewer note:</strong> " + reason + "</p>"
	}
	html := "<p>Hi " + name + ",</p>" +
		"<p>We couldn&rsquo;t verify the payment proof you uploaded for order <strong>#" + short + "</strong>.</p>" +
		reasonBlock +
		"<p>Please open the order in your account and re-upload a clearer screenshot or the transaction hash. We&rsquo;ll activate the order as soon as we can confirm the deposit.</p>" +
		"<p>— Viralefy</p>"
	text := "Hi " + name + ",\n\nWe couldn't verify the payment proof you uploaded for order #" + short + ".\n"
	if reason != "" {
		text += "Reviewer note: " + reason + "\n"
	}
	text += "\nPlease re-upload a clearer screenshot or your transaction hash from your account.\n\n— Viralefy"
	return application.EmailMessage{
		To:       to,
		Subject:  "Payment proof needs another look — Order #" + short,
		HTMLBody: html,
		TextBody: text,
	}
}

// AdminListPendingProofs — GET /v1/admin/proofs/pending
// Fila de comprovantes aguardando revisão (mais antigos primeiro).
// Backoffice usa pra atacar SLA: cliente fica pendurado esperando
// aprovação manual; queremos saber quem está esperando mais.
func (h *Handlers) AdminListPendingProofs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	list, err := h.Orders.ListPendingProofs(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, list)
}

// allowedProofMIME — whitelist conservadora. Anything outside é rejeitado.
// Mantém em sync com a accept= do input file no front (image/*,application/pdf).
var allowedProofMIME = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/webp":      ".webp",
	"image/gif":       ".gif",
	"application/pdf": ".pdf",
}

const proofMaxBytes = 5 << 20 // 5 MB

// uploadProofMultipart processa multipart/form-data: field "file" (binary),
// opcional "note" (text). Faz PutObject no MinIO/R2 com key
// proofs/{order_id}/{ts}-{rand}{ext} e persiste só essa key em proof_url.
// Se storage está disabled, retorna 503 — front deve cair no fluxo legacy.
func (h *Handlers) uploadProofMultipart(w http.ResponseWriter, r *http.Request, userID, orderID string) {
	if h.Storage == nil {
		writeError(w, application.ErrStorageDisabled)
		return
	}
	// MaxBytesReader engole o body inteiro até o limite — anti DoS.
	r.Body = http.MaxBytesReader(w, r.Body, proofMaxBytes+512)
	if err := r.ParseMultipartForm(proofMaxBytes); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	defer file.Close()
	if header.Size > proofMaxBytes {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	mime := strings.ToLower(strings.TrimSpace(header.Header.Get("Content-Type")))
	ext, ok := allowedProofMIME[mime]
	if !ok {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	// Key: proofs/{order}/{ts}-{rand}.{ext}. ts dá ordem; rand evita
	// colisão se cliente subir 2 dentro do mesmo segundo (improvável mas
	// defensivo). order/ prefix permite listing per-order pra debug.
	key := "proofs/" + orderID + "/" + nowKeyPrefix() + ext
	storedKey, err := h.Storage.Put(r.Context(), "proofs", key, file, header.Size, mime)
	if err != nil {
		writeError(w, err)
		return
	}
	// storedKey vai em DOIS lugares: proof_url (retro-compat com leitores
	// antigos que não conhecem proof_storage_key ainda) E proof_storage_key
	// (fonte canônica pós-rollout 040). Reader prefere o segundo.
	if err := h.Orders.SetProof(
		r.Context(), orderID, storedKey, header.Filename, mime, note, int(header.Size), storedKey,
	); err != nil {
		writeError(w, err)
		return
	}
	updated, err := h.OrderSvc.GetByIDForUser(r.Context(), userID, orderID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, updated)
}

// nowKeyPrefix gera "20260608T193045-a1b2c3d4" — timestamp UTC + 8 chars
// random pra unicidade dentro do bucket.
func nowKeyPrefix() string {
	ts := time.Now().UTC().Format("20060102T150405")
	buf := make([]byte, 4)
	_, _ = io.ReadFull(rand.Reader, buf)
	return ts + "-" + hex.EncodeToString(buf)
}

// MeGetProofURL — GET /v1/me/orders/{id}/proof-url
// Retorna presigned URL pra cliente baixar/visualizar o próprio comprovante.
// Útil pro user revisar antes de submit, ou pra app mobile renderizar inline.
func (h *Handlers) MeGetProofURL(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	orderID := chi.URLParam(r, "id")
	o, err := h.OrderSvc.GetByIDForUser(r.Context(), userID, orderID)
	if err != nil {
		writeError(w, err)
		return
	}
	url, err := h.resolveProofURL(r.Context(), o)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"url": url})
}

// AdminGetProofURL — GET /v1/admin/orders/{id}/proof-url
// Espelha pro admin — backoffice ProofCard chama isso pra renderizar img/<a>.
func (h *Handlers) AdminGetProofURL(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "id")
	o, err := h.Orders.GetByID(r.Context(), orderID)
	if err != nil || o == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	url, err := h.resolveProofURL(r.Context(), o)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"url": url})
}

// resolveProofURL devolve a melhor URL pra renderizar o comprovante,
// respeitando a precedência pós-migração 040:
//
//  1. proof_storage_key NOT NULL → presign MinIO/R2 (fonte canônica).
//     Migrador offline (cmd/migrate-proofs) preenche essa coluna pra
//     proofs legados; uploads novos preenchem direto via SetProof.
//  2. proof_url começa com data: ou http(s):// → devolve crua (legacy:
//     base64 inline ou URL hospedada em terceiro tipo imgur). Necessário
//     enquanto o migrador não rodou em prod ainda.
//  3. proof_url é uma key opaca (sem prefixo conhecido) → presign via
//     Storage. Cobre o caso em que o upload novo gravou só proof_url=key
//     mas o reader subiu antes do migration 040 chegar no DB.
//  4. caso contrário → NotFound.
func (h *Handlers) resolveProofURL(ctx context.Context, o *domain.Order) (string, error) {
	if o == nil {
		return "", domain.ErrNotFound
	}
	// Precedência 1: proof_storage_key (caminho rápido pós-migração).
	if o.ProofStorageKey != nil && *o.ProofStorageKey != "" {
		if h.Storage == nil {
			return "", application.ErrStorageDisabled
		}
		return h.Storage.PresignedGetURL(ctx, "proofs", *o.ProofStorageKey, 5*time.Minute)
	}
	if o.ProofURL == nil || *o.ProofURL == "" {
		return "", domain.ErrNotFound
	}
	raw := *o.ProofURL
	// Precedência 2: URL crua (legacy data:URL ou http externo).
	if strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw, nil
	}
	// Precedência 3: key opaca em proof_url.
	if h.Storage == nil {
		return "", application.ErrStorageDisabled
	}
	return h.Storage.PresignedGetURL(ctx, "proofs", raw, 5*time.Minute)
}

// =====================================================================
// 2FA — admin enroll + verify + login flow
// =====================================================================

// AdminLoginEnroll2FA — POST /v1/auth/login/2fa/enroll
// Chamado quando AdminLogin retornou twofa_enroll_required=true. Body:
// {partial_token}. Gera secret novo + 8 backup codes, persiste cifrado.
// O wizard mostra QR + codes UMA vez; verificação real acontece via
// AdminLoginComplete2FA com o primeiro código TOTP.
func (h *Handlers) AdminLoginEnroll2FA(w http.ResponseWriter, r *http.Request) {
	if h.AdminTwoFA == nil {
		writeError(w, domain.ErrNotImplemented)
		return
	}
	var body struct {
		PartialToken string `json:"partial_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	adminID, err := h.Auth.ParsePartialToken(body.PartialToken)
	if err != nil {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	admin, err := h.Auth.GetAdminByID(r.Context(), adminID)
	if err != nil || admin == nil {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	res, err := h.AdminTwoFA.Enroll(r.Context(), adminID, admin.Email)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, res)
}

// AdminLoginComplete2FA — POST /v1/auth/login/2fa
// Segundo step do login. Body: {partial_token, code}. code é TOTP de 6
// dígitos OU backup code 10 chars. Verifica e retorna o LoginResult final
// (mesmo shape do AdminLogin happy path).
func (h *Handlers) AdminLoginComplete2FA(w http.ResponseWriter, r *http.Request) {
	if h.AdminTwoFA == nil {
		writeError(w, domain.ErrNotImplemented)
		return
	}
	var body struct {
		PartialToken string `json:"partial_token"`
		Code         string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	res, err := h.Auth.CompleteLoginWith2FA(r.Context(), body.PartialToken, body.Code)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, res)
}

// AdminDisable2FA — POST /v1/admin/me/2fa/disable
// Apenas superadmin (PermAdminsManage). Útil pra reset quando admin perdeu
// device + backup codes. Audit log gravado explicitamente — operação rara e
// crítica que precisa de trilha permanente.
func (h *Handlers) AdminDisable2FA(w http.ResponseWriter, r *http.Request) {
	if h.AdminTwoFA == nil {
		writeError(w, domain.ErrNotImplemented)
		return
	}
	var body struct {
		AdminID string `json:"admin_id"`
		Reason  string `json:"reason,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if body.AdminID == "" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	actor, _ := principalFromContext(r.Context())
	if err := h.AdminTwoFA.Disable(r.Context(), body.AdminID); err != nil {
		writeError(w, err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(r.Context(), application.AuditEntry{
			ActorType:  "admin",
			ActorID:    actor.AdminID,
			Action:     "admin.2fa.disable",
			TargetType: "admin",
			TargetID:   body.AdminID,
			Metadata:   map[string]any{"reason": body.Reason},
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdminBulkProofDecision — POST /v1/admin/proofs/bulk-decision
// Body {order_ids: [], decision: "approved"|"rejected", note?}. Limita
// 50 orders/call (anti foot-gun). Cada decisão é gravada individualmente
// pra audit + idempotency. Retorna lista de {order_id, status} por linha.
func (h *Handlers) AdminBulkProofDecision(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrderIDs []string `json:"order_ids"`
		Decision string   `json:"decision"`
		Note     string   `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	body.Decision = strings.ToLower(strings.TrimSpace(body.Decision))
	if body.Decision != "approved" && body.Decision != "rejected" {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if len(body.OrderIDs) == 0 || len(body.OrderIDs) > 50 {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	type rowResult struct {
		OrderID string `json:"order_id"`
		Status  string `json:"status"`
		Reason  string `json:"reason,omitempty"`
	}
	results := make([]rowResult, 0, len(body.OrderIDs))
	actor, _ := principalFromContext(r.Context())
	for _, id := range body.OrderIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		res := rowResult{OrderID: id, Status: "applied"}
		o, err := h.Orders.GetByID(r.Context(), id)
		if err != nil || o == nil {
			res.Status, res.Reason = "skipped", "not found"
			results = append(results, res)
			continue
		}
		if o.ProofURL == nil || *o.ProofURL == "" {
			res.Status, res.Reason = "skipped", "no proof attached"
			results = append(results, res)
			continue
		}
		if err := h.Orders.SetProofStatus(r.Context(), id, body.Decision, body.Note); err != nil {
			res.Status, res.Reason = "error", err.Error()
			results = append(results, res)
			continue
		}
		if body.Decision == "approved" {
			if err := h.PaymentReceiver.MarkOrderPaid(r.Context(), id); err != nil {
				res.Status, res.Reason = "error", err.Error()
				results = append(results, res)
				continue
			}
		} else if h.Email != nil {
			if user, _ := h.Users.GetByID(r.Context(), o.UserID); user != nil {
				_ = h.Email.Send(r.Context(), buildProofRejectionEmail(user.Name, user.Email, id, body.Note))
			}
		}
		if h.Audit != nil {
			h.Audit.Log(r.Context(), application.AuditEntry{
				ActorType:  "admin",
				ActorID:    actor.AdminID,
				Action:     "proof.bulk." + body.Decision,
				TargetType: "order",
				TargetID:   id,
				Metadata:   map[string]any{"note": body.Note},
			})
		}
		results = append(results, res)
	}
	writeData(w, http.StatusOK, map[string]any{"results": results})
}

// =====================================================================
// 2FA — user enroll + verify + login + dismiss prompt
// =====================================================================

// UserLoginComplete2FA — POST /v1/auth/user/login/2fa
// Body {partial_token, code}. Espelha o admin /auth/login/2fa.
func (h *Handlers) UserLoginComplete2FA(w http.ResponseWriter, r *http.Request) {
	if h.UserTwoFA == nil {
		writeError(w, domain.ErrNotImplemented)
		return
	}
	var body struct {
		PartialToken string `json:"partial_token"`
		Code         string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	sess, err := h.UserAuth.CompleteLoginWith2FA(r.Context(), body.PartialToken, body.Code)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, sess)
}

// MeEnroll2FA — POST /v1/me/2fa/enroll
// User logado pede setup. Gera secret + 8 backup codes UMA vez. Re-enroll
// antes do primeiro Verify sobrescreve (sem deixar 2FA half-on).
func (h *Handlers) MeEnroll2FA(w http.ResponseWriter, r *http.Request) {
	if h.UserTwoFA == nil {
		writeError(w, domain.ErrNotImplemented)
		return
	}
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	user, err := h.Users.GetByID(r.Context(), userID)
	if err != nil || user == nil {
		writeError(w, domain.ErrNotFound)
		return
	}
	res, err := h.UserTwoFA.Enroll(r.Context(), userID, user.Email)
	if err != nil {
		writeError(w, err)
		return
	}
	writeData(w, http.StatusOK, res)
}

// MeVerify2FA — POST /v1/me/2fa/verify
// Primeira verificação após Enroll → marca enrolled_at. Subsequentes
// verificações no login passam por UserLoginComplete2FA, não aqui.
func (h *Handlers) MeVerify2FA(w http.ResponseWriter, r *http.Request) {
	if h.UserTwoFA == nil {
		writeError(w, domain.ErrNotImplemented)
		return
	}
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	if err := h.UserTwoFA.Verify(r.Context(), userID, body.Code); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MeDisable2FA — POST /v1/me/2fa/disable
// User self-service desativa o próprio 2FA. Diferente do admin (que precisa
// passar pelo superadmin), o user pode desabilitar próprio.
func (h *Handlers) MeDisable2FA(w http.ResponseWriter, r *http.Request) {
	if h.UserTwoFA == nil {
		writeError(w, domain.ErrNotImplemented)
		return
	}
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if err := h.UserTwoFA.Disable(r.Context(), userID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MeDismiss2FAPrompt — POST /v1/me/2fa/dismiss-prompt
// User clicou "Talvez depois" no modal de nag. Incrementa counter +
// timestamp pra cooldown progressivo.
func (h *Handlers) MeDismiss2FAPrompt(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	if h.DB == nil {
		writeError(w, domain.ErrNotImplemented)
		return
	}
	if _, err := h.DB.Pool().Exec(r.Context(),
		`UPDATE users
		    SET twofa_prompt_dismissed_count = twofa_prompt_dismissed_count + 1,
		        twofa_prompt_last_dismissed_at = NOW()
		  WHERE id = $1`, userID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MeTwoFAStatus — GET /v1/me/2fa/status
// Front consulta na primeira tela pós-login. Retorna {enrolled, should_prompt}.
//
// should_prompt = true sse TODOS true:
//   - user NÃO está enrolled
//   - user TEM ≥1 order com status='paid' AND delivery_captured_at != NULL
//   - dismiss_count < 5 OU último_dismiss > 7d atrás
//
// "TEM ≥1 paid+delivered" é o gate de "atormentar". Sem dado sensível,
// foda-se — não enche o saco.
func (h *Handlers) MeTwoFAStatus(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	if userID == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	enrolled := false
	if h.UserTwoFA != nil {
		enrolled = h.UserTwoFA.IsEnrolled(r.Context(), userID)
	}
	shouldPrompt := false
	if !enrolled && h.DB != nil {
		shouldPrompt = h.computeShouldPrompt2FA(r.Context(), userID)
	}
	writeData(w, http.StatusOK, map[string]any{
		"enrolled":      enrolled,
		"should_prompt": shouldPrompt,
	})
}

func (h *Handlers) computeShouldPrompt2FA(ctx context.Context, userID string) bool {
	// Query única — evita 2 round-trips. Conta orders elegíveis + lê
	// dismiss state na mesma row.
	var hasCompleted bool
	var dismissedCount int
	var lastDismissed *time.Time
	row := h.DB.Pool().QueryRow(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1 FROM orders
		     WHERE user_id = $1
		       AND status = 'paid'
		       AND delivery_captured_at IS NOT NULL
		  ),
		  COALESCE(u.twofa_prompt_dismissed_count, 0),
		  u.twofa_prompt_last_dismissed_at
		  FROM users u WHERE u.id = $1`, userID)
	if err := row.Scan(&hasCompleted, &dismissedCount, &lastDismissed); err != nil {
		return false
	}
	if !hasCompleted {
		return false
	}
	if dismissedCount < 5 {
		return true
	}
	// 5+ dismissals: espera 7 dias entre prompts.
	if lastDismissed == nil {
		return true
	}
	return time.Since(*lastDismissed) > 7*24*time.Hour
}
