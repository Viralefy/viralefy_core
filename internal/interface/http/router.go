package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// ReadyChecker é a dependência opcional do /ready: deve devolver nil quando o
// processo está pronto para tráfego (ex.: db.Ping). Pode ser nil — neste caso
// /ready vira liveness simples e devolve 200.
type ReadyChecker func(r *http.Request) error

func NewRouter(h *Handlers, corsOrigins []string, ready ReadyChecker, adminAuth, userAuth, optionalUserAuth func(http.Handler) http.Handler, internalToken string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	// OTel HTTP middleware: cria span para cada request, propaga W3C trace
	// context, popula trace_id no contexto. Tem que vir ANTES do nosso
	// ObservabilityMiddleware, que lê o span do contexto.
	r.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "viralefy-api",
			otelhttp.WithSpanNameFormatter(func(_ string, req *http.Request) string {
				return req.Method + " " + req.URL.Path
			}),
		)
	})
	r.Use(ObservabilityMiddleware)
	// Sentry antes do Recoverer pra capturar o panic com a request context;
	// Recoverer ainda responde 500 ao client (Repanic=true entrega ao próximo).
	r.Use(observability.SentryMiddleware())
	r.Use(middleware.Recoverer)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   corsOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Idempotency-Key"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/health", Health)
	r.Method(http.MethodGet, "/ready", ReadyHandler(ready))
	r.Method(http.MethodGet, "/metrics", observability.MetricsHandler())
	// JWKS pública (RS256) — fora de /v1 pra ser descobrível como RFC 8615.
	r.Get("/.well-known/jwks.json", h.PublicJWKS)

	// Middlewares de segurança aplicados a mutations sensíveis (checkout
	// e recovery): idempotência por header Idempotency-Key e rate-limit
	// 30 req/min/IP (anti-abuso da API, não anti-spam de email — esse
	// gargalo já é trancado pelo fluxo "comunicação só pós-pagamento").
	idem := IdempotencyMiddleware(h.DB)
	mutationLimiter := NewRateLimiter(30, time.Minute).Middleware()
	// Login rate-limit: 10 tentativas / 15 min por IP. Cobre admin login,
	// user login e register no mesmo bucket — atacante que tenta os 3
	// alternando bate no mesmo limite. Turnstile bloqueia bots não-humanos;
	// este limite protege contra password spray pós-Turnstile-solved.
	loginLimiter := NewRateLimiter(10, 15*time.Minute).Middleware()

	r.Route("/v1", func(r chi.Router) {
		// Público
		r.Get("/plans", h.ListPublicPlans)
		r.Get("/plans/{id}/reviews", h.PublicReviewsForPlan)
		r.Get("/plans/{id}/payment-methods", h.PublicListPaymentMethods)
		r.Get("/categories", h.ListCategories)
		r.Get("/categories/{code}/reviews", h.PublicReviewsForCategory)
		r.Get("/currencies", h.ListCurrencies)
		r.Get("/status", h.PublicStatus)
		r.Get("/country-ppp", h.PublicCountryPPP)
		r.With(mutationLimiter).Post("/coupons/validate", h.PublicValidateCoupon)
		r.Get("/referrals/{code}/info", h.PublicReferralInfo)
		r.With(mutationLimiter).Post("/ab/assign", h.PublicABAssign)
		r.With(mutationLimiter).Post("/ab/track", h.PublicABTrack)
		r.With(mutationLimiter).Post("/track", h.PublicTrackEvent)
		// Cookie consent audit log (LGPD Art. 8 §6). Aceita anônimo —
		// optionalUserAuth correlaciona com user_id quando o JWT estiver
		// presente; sem token o handler grava só visitor_id.
		r.With(mutationLimiter, optionalUserAuth).Post("/me/consent", h.PublicRecordConsent)
		r.Get("/tax-rates", h.PublicTaxRates)
		// Checkout aceita token opcional — quando logado, usa profile_id e
		// pode pagar com créditos. Sem token, cria conta na hora.
		r.With(mutationLimiter, idem, optionalUserAuth).Post("/checkout", h.CreateCheckout)

		// Account Recovery: aceita formulário (data do banimento, motivo,
		// última publicação) e dispara checkout do plano de recuperação;
		// após payment, abre ticket automático com snapshot do form.
		// Protegido por Turnstile + rate-limit + idempotência.
		r.With(mutationLimiter, idem, optionalUserAuth).Post("/recovery-request", h.CreateRecoveryRequest)

		// Webhooks dos providers (sem auth — assinatura é validada no handler).
		r.Post("/webhooks/woovi", h.WooviWebhook)
		r.Post("/webhooks/heleket", h.HeleketWebhook)
		r.Post("/webhooks/stripe", h.StripeWebhook)
		r.Post("/webhooks/resend", h.ResendWebhook)

		// Auth admin (backoffice)
		r.With(loginLimiter).Post("/auth/login", h.AdminLogin)
		// 2FA flow: enroll roda na partial_token; complete (login final)
		// roda também na partial_token. Ambos atrás do loginLimiter pra
		// proteger contra brute-force do código TOTP.
		r.With(loginLimiter).Post("/auth/login/2fa/enroll", h.AdminLoginEnroll2FA)
		r.With(loginLimiter).Post("/auth/login/2fa", h.AdminLoginComplete2FA)

		// Auth de usuário (loja)
		r.With(loginLimiter).Post("/auth/user/register", h.UserRegister)
		r.With(loginLimiter).Post("/auth/user/login", h.UserLogin)
		r.With(loginLimiter).Post("/auth/user/login/2fa", h.UserLoginComplete2FA)

		// Área logada do usuário
		r.Route("/me", func(r chi.Router) {
			r.Use(userAuth)
			r.Get("/orders", h.MeOrders)
			r.Get("/orders/{id}", h.MeGetOrder)
			r.With(mutationLimiter, idem).Post("/orders/{id}/proof", h.MeUploadProof)
			r.Get("/orders/{id}/proof-url", h.MeGetProofURL)
			r.Get("/referral", h.MeGetMyReferral)
			r.Get("/journey", h.MeJourney)

			// 2FA — opt-in pra user. status devolve {enrolled, should_prompt}.
			r.Get("/2fa/status", h.MeTwoFAStatus)
			r.With(mutationLimiter).Post("/2fa/enroll", h.MeEnroll2FA)
			r.With(mutationLimiter).Post("/2fa/verify", h.MeVerify2FA)
			r.With(mutationLimiter).Post("/2fa/disable", h.MeDisable2FA)
			r.With(mutationLimiter).Post("/2fa/dismiss-prompt", h.MeDismiss2FAPrompt)

			r.Get("/subscriptions", h.MeListMySubscriptions)
			r.Post("/subscriptions", h.MeSubscribe)
			r.Delete("/subscriptions/{id}", h.MeCancelSubscription)

			r.Get("/whatsapp", h.MeGetWhatsAppPref)
			r.Put("/whatsapp", h.MeUpdateWhatsApp)

			r.Get("/api-keys", h.MeListAPIKeys)
			r.Post("/api-keys", h.MeCreateAPIKey)
			r.Delete("/api-keys/{id}", h.MeRevokeAPIKey)

			r.Get("/notif-prefs", h.MeGetNotifPrefs)
			r.Put("/notif-prefs", h.MeUpdateNotifPrefs)

			r.Get("/data/export", h.MeExportData)
			r.Get("/data/deletion", h.MeGetDeletion)
			r.Post("/data/deletion", h.MeRequestDeletion)
			r.Delete("/data/deletion", h.MeCancelDeletion)

			r.Get("/profiles", h.MeListProfiles)
			r.Post("/profiles", h.MeAddProfile)
			r.Delete("/profiles/{id}", h.MeDeleteProfile)

			r.Get("/credits", h.MeCredits)
			r.Get("/transactions", h.MeTransactions)
			r.Post("/recharge", h.MeRecharge)
			r.Get("/invoices", h.MeListInvoices)

			r.Get("/tickets", h.MeListTickets)
			r.Get("/tickets/open-count", h.MeOpenTicketsCount)
			r.Post("/tickets", h.MeCreateTicket)
			r.Get("/tickets/{id}", h.MeGetTicket)
			r.Post("/tickets/{id}/messages", h.MeReplyTicket)

			// Reviews — submit post-delivery + read own.
			r.With(mutationLimiter, idem).Post("/reviews", h.MeCreateReview)
			r.Get("/reviews/by-order/{order_id}", h.MeGetReviewForOrder)
		})

		// Admin — RBAC: cada rota exige uma permissão (após AdminAuth).
		r.Route("/admin", func(r chi.Router) {
			r.Use(adminAuth)

			r.Get("/me", h.AdminMe)
			r.Post("/me/become-customer", h.AdminBecomeCustomer)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/me/2fa/disable", h.AdminDisable2FA)
			r.With(RequirePermission(domain.PermAdminsManage)).Get("/roles", h.AdminListRoles)

			// Gestão de admins (RBAC manager). Único admins:manage que
			// modifica tabela admins. Self-delete + cross-role promotion
			// são bloqueados pelo service (defense-in-depth).
			r.With(RequirePermission(domain.PermAdminsManage)).Get("/admins", h.AdminListAdmins)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/admins", h.AdminCreateAdmin)
			r.With(RequirePermission(domain.PermAdminsManage)).Put("/admins/{id}", h.AdminUpdateAdmin)
			r.With(RequirePermission(domain.PermAdminsManage)).Delete("/admins/{id}", h.AdminDeleteAdmin)

			r.With(RequirePermission(domain.PermPlansRead)).Get("/plans", h.AdminListPlans)
			r.With(RequirePermission(domain.PermPlansWrite)).Post("/plans", h.AdminCreatePlan)
			r.With(RequirePermission(domain.PermPlansWrite)).Put("/plans/{id}", h.AdminUpdatePlan)
			r.With(RequirePermission(domain.PermPlansWrite)).Delete("/plans/{id}", h.AdminDeletePlan)

			r.With(RequirePermission(domain.PermGatewaysRead)).Get("/gateways", h.AdminListGateways)
			r.With(RequirePermission(domain.PermGatewaysWrite)).Post("/gateways", h.AdminCreateGateway)
			r.With(RequirePermission(domain.PermGatewaysWrite)).Put("/gateways/{id}", h.AdminUpdateGateway)
			r.With(RequirePermission(domain.PermGatewaysWrite)).Delete("/gateways/{id}", h.AdminDeleteGateway)

			r.With(RequirePermission(domain.PermOrdersRead)).Get("/orders", h.AdminListOrders)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/orders/{id}", h.AdminGetOrder)
			// PATCH muda status/notes; mark-paid já existe à parte como ação
			// específica (chama PaymentReceiver pra disparar hooks).
			r.With(RequirePermission(domain.PermAdminsManage)).Patch("/orders/{id}", h.AdminPatchOrder)
			// Capture manual de baseline/delivery metrics. POST body opcional:
			// {"kind":"baseline"|"delivery"} — default baseline.
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/orders/{id}/capture-metrics", h.AdminCaptureOrderMetrics)
			// Métricas agregadas pro /dashboard.
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/metrics/summary", h.AdminMetricsSummary)

			r.With(RequirePermission(domain.PermCurrenciesRead)).Get("/currencies", h.AdminListCurrencies)
			r.With(RequirePermission(domain.PermCurrenciesWrite)).Put("/currencies/{code}", h.AdminUpdateCurrency)

			r.With(RequirePermission(domain.PermTicketsRead)).Get("/tickets", h.AdminListTickets)
			r.With(RequirePermission(domain.PermTicketsRead)).Get("/tickets/{id}", h.AdminGetTicket)
			r.With(RequirePermission(domain.PermTicketsWrite)).Post("/tickets/{id}/messages", h.AdminReplyTicket)
			r.With(RequirePermission(domain.PermTicketsWrite)).Patch("/tickets/{id}", h.AdminUpdateTicket)

			// Invoices (recargas). Marcar como paga é sensível → admins:manage.
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/invoices", h.AdminListInvoices)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/invoices/{id}", h.AdminGetInvoice)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/invoices/{id}/mark-paid", h.AdminMarkInvoicePaid)

			// Reviews (moderação).
			r.With(RequirePermission(domain.PermReviewsRead)).Get("/reviews", h.AdminListReviews)
			r.With(RequirePermission(domain.PermReviewsModerate)).Patch("/reviews/{id}", h.AdminPatchReviewVisibility)

			// Coupons.
			r.With(RequirePermission(domain.PermCouponsRead)).Get("/coupons", h.AdminListCoupons)
			r.With(RequirePermission(domain.PermCouponsWrite)).Post("/coupons", h.AdminCreateCoupon)
			r.With(RequirePermission(domain.PermCouponsWrite)).Put("/coupons/{code}", h.AdminUpdateCoupon)

			// Anti-fraude + A/B testing + refunds.
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/fraud/signals", h.AdminListFraudSignals)
			r.With(RequirePermission(domain.PermAdminsManage)).Get("/ab/experiments", h.AdminListAB)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/ab/experiments", h.AdminCreateAB)
			r.With(RequirePermission(domain.PermAdminsManage)).Put("/ab/experiments/{key}", h.AdminUpdateAB)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/orders/{id}/refund", h.AdminIssueRefund)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/orders/{id}/refunds", h.AdminListOrderRefunds)

			// Vendors (multi-vendor scaffold).
			r.With(RequirePermission(domain.PermAdminsManage)).Get("/vendors", h.AdminListVendors)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/vendors", h.AdminCreateVendor)
			r.With(RequirePermission(domain.PermAdminsManage)).Put("/vendors/{id}", h.AdminUpdateVendor)

			// Usuários, ajuste de saldo e marcação manual de pedido.
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/users", h.AdminListUsers)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/users/{id}", h.AdminGetUser)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/users/{id}/journey", h.AdminUserJourney)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/visitors", h.AdminListVisitors)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/visitors/{vid}", h.AdminGetVisitor)

			// SOFT delete (admin com PermAdminsManage). HARD delete + RESTORE
			// (superadmin only). 3 entidades: orders, invoices, users.
			r.With(RequirePermission(domain.PermAdminsManage)).Delete("/orders/{id}", h.AdminSoftDeleteOrder)
			r.With(RequireSuperadmin).Delete("/orders/{id}/hard", h.AdminHardDeleteOrder)
			r.With(RequireSuperadmin).Post("/orders/{id}/restore", h.AdminRestoreOrder)

			r.With(RequirePermission(domain.PermAdminsManage)).Delete("/invoices/{id}", h.AdminSoftDeleteInvoice)
			r.With(RequireSuperadmin).Delete("/invoices/{id}/hard", h.AdminHardDeleteInvoice)
			r.With(RequireSuperadmin).Post("/invoices/{id}/restore", h.AdminRestoreInvoice)

			r.With(RequirePermission(domain.PermAdminsManage)).Delete("/users/{id}", h.AdminSoftDeleteUser)
			r.With(RequireSuperadmin).Delete("/users/{id}/hard", h.AdminHardDeleteUser)
			r.With(RequireSuperadmin).Post("/users/{id}/restore", h.AdminRestoreUser)

			// Trash — aba consolidada de tudo que admin apagou. Só
			// superadmin acessa; oculto do fluxo normal.
			r.With(RequireSuperadmin).Get("/trash", h.AdminTrash)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/users/{id}/credits/adjust", h.AdminAdjustCredits)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/orders/{id}/mark-paid", h.AdminMarkOrderPaid)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/orders/{id}/proof/decision", h.AdminProofDecision)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/proofs/pending", h.AdminListPendingProofs)
			r.With(RequirePermission(domain.PermAdminsManage)).Post("/proofs/bulk-decision", h.AdminBulkProofDecision)
			r.With(RequirePermission(domain.PermOrdersRead)).Get("/orders/{id}/proof-url", h.AdminGetProofURL)
		})
	})

	// API B2B v2 — autenticada via X-API-Key header. Read-only por enquanto;
	// rate-limit per-key e billing per-call ficam para v2.5.
	r.Route("/v2", func(r chi.Router) {
		r.Use(apiKeyAuth(h.APIKeys))
		r.Get("/plans", h.PublicV2Plans)
		r.Get("/orders/{id}/status", h.PublicV2OrderStatus)
	})

	// /internal/v1/* — callbacks dos microsserviços (PHASE-8 §1). NÃO
	// expostos via Caddy; bind loopback-only no main.go. X-Internal-Token
	// é defense-in-depth — qualquer request sem token ou com token errado
	// vira 401 antes de tocar no handler. Sem userAuth/adminAuth (esses
	// fazem sentido só pra mundo externo).
	r.Route("/internal/v1", func(r chi.Router) {
		r.Use(InternalTokenAuth(internalToken))
		r.Post("/payment-confirmed", h.InternalPaymentConfirmed)
	})

	return r
}
