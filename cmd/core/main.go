package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Viralefy/viralefy_core/internal/application"
	"github.com/Viralefy/viralefy_core/internal/config"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/email"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/jwtkeys"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/notify"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/payment"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/paymentsclient"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/senderclient"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/storage"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/turnstile"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/persistence/postgres"
	httphandler "github.com/Viralefy/viralefy_core/internal/interface/http"
)

func main() {
	// Subcomandos de operação que não sobem o servidor HTTP. Mantidos no
	// mesmo binário pra reusar config/observability/db pool — viralefy-api
	// vira a "porta única" pro ops e dev.
	//
	//   viralefy-api migrate status   — lista migrations + estado
	//   viralefy-api migrate up       — aplica pendentes (idempotente)
	//   viralefy-api migrate backfill — marca todas como aplicadas SEM rodar
	//                                   (uso UMA vez em prod existente)
	//   viralefy-api seed             — roda Seed() explicitamente (opt-in)
	//   viralefy-api (sem args)       — sobe o servidor HTTP normal
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "migrate":
			runMigrateCmd()
			return
		case "seed":
			runSeedCmd()
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	// ---- Observabilidade (logger, métricas, traces) ---- //
	// Versão é injetada via -ldflags em release builds; default "dev" em local.
	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "dev"
	}
	logger := observability.InitLogger(observability.LoggerConfig{
		Level:     slog.LevelInfo,
		Service:   "viralefy-api",
		Version:   version,
		Component: "api",
	})
	observability.InitMetrics()

	// Sentry — no-op se SENTRY_DSN vazio. Flush no shutdown gracioso.
	shutdownSentry := observability.InitSentry("viralefy-api", version, os.Getenv("APP_ENV"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = shutdownSentry(ctx)
		cancel()
	}()

	tracerCtx, tracerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownTracer, err := observability.InitTracer(tracerCtx, observability.TracingConfig{
		ServiceName:    "viralefy-api",
		ServiceVersion: version,
		Environment:    os.Getenv("APP_ENV"),
	})
	tracerCancel()
	if err != nil {
		// Tracing é não-bloqueante: se Tempo não estiver pronto, só logamos e
		// seguimos sem traces. A métrica/log ainda capturam tudo.
		logger.Warn("tracer init failed; continuing without traces", "error", err.Error())
		shutdownTracer = func(context.Context) error { return nil }
	}

	ctx := context.Background()
	db, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connect failed", "error", err.Error())
		log.Fatal("database:", err)
	}
	defer db.Close()

	if err := postgres.RunMigrations(ctx, db); err != nil {
		log.Fatal("migrate:", err)
	}
	// Seed NÃO roda mais automático em boot — era a causa do incidente
	// "marketplace items voltam" e "preços que admin editou voltam" (UPSERT
	// destrutivo a cada deploy). Pra seedar:
	//
	//   viralefy-api seed         — roda Seed() (idempotente, DO NOTHING)
	//
	// O comando é seguro pra rodar em qualquer momento — não sobrescreve
	// nada que já existe.

	planRepo := postgres.NewPlanRepo(db)
	userRepo := postgres.NewUserRepo(db)
	orderRepo := postgres.NewOrderRepo(db)
	gwRepo := postgres.NewGatewayRepo(db)
	adminRepo := postgres.NewAdminRepo(db)
	roleRepo := postgres.NewRoleRepo(db)
	categoryRepo := postgres.NewCategoryRepo(db)
	currencyRepo := postgres.NewCurrencyRepo(db)
	couponRepo := postgres.NewCouponRepo(db)
	countryPPPRepo := postgres.NewCountryPPPRepo(db)
	referralRepo := postgres.NewReferralRepo(db)
	abRepo := postgres.NewABTestRepo(db)
	subscriptionRepo := postgres.NewSubscriptionRepo(db)
	taxRateRepo := postgres.NewTaxRateRepo(db)
	vendorRepo := postgres.NewVendorRepo(db)
	apiKeyRepo := postgres.NewAPIKeyRepo(db)
	userEventRepo := postgres.NewUserEventRepo(db)
	ticketRepo := postgres.NewTicketRepo(db)
	profileRepo := postgres.NewProfileRepo(db)
	creditRepo := postgres.NewCreditRepo(db)
	invoiceRepo := postgres.NewInvoiceRepo(db)

	// PHASE-8 Wave 3: quando SENDER_INTERNAL_URL setado, troca o
	// emailSender concreto pelo senderclient (HTTP → viralefy_sender). O
	// senderclient implementa application.EmailSender (Send passthrough) E
	// application.TemplatedEmailer (SendTemplate pra checkout_paid).
	// Vazio = legacy SMTP/Resend direto.
	var emailSender application.EmailSender
	var senderRemote *senderclient.Client
	if cfg.SenderInternalURL != "" {
		senderRemote = senderclient.New(cfg.SenderInternalURL, cfg.InternalSharedSecret)
		emailSender = senderRemote
		logger.Info("email sender: remote viralefy_sender",
			"url", cfg.SenderInternalURL)
	} else {
		emailSender = email.New(email.Config{
			Provider:       cfg.EmailProvider,
			Addr:           cfg.SMTPAddr,
			User:           cfg.SMTPUser,
			Pass:           cfg.SMTPPass,
			From:           cfg.SMTPFrom,
			FromName:       cfg.SMTPFromName,
			ResendAPIKey:   cfg.ResendAPIKey,
			ResendFrom:     cfg.ResendFrom,
			ResendFromName: cfg.ResendFromName,
			ResendBaseURL:  cfg.ResendBaseURL,
		})
		logger.Info("email sender: legacy (SMTP/Resend direto)")
	}

	// PHASE-8 Wave 3: quando PAYMENTS_INTERNAL_URL setado, troca o
	// PaymentRegistry pelo modo remoto — todo CreateCharge cai no
	// paymentsclient (HTTP → viralefy_payments). Os providers in-memory
	// (stripe/woovi/etc) ficam fora do registry; arquivos do package
	// payment/ continuam no repo (compat com migration legado + tests).
	var payments *application.PaymentRegistry
	var paymentsRemote *paymentsclient.Client
	if cfg.PaymentsInternalURL != "" {
		paymentsRemote = paymentsclient.New(cfg.PaymentsInternalURL, cfg.InternalSharedSecret)
		payments = application.NewRemotePaymentRegistry(paymentsRemote)
		logger.Info("payments: remote viralefy_payments",
			"url", cfg.PaymentsInternalURL)
	} else {
		payments = application.NewPaymentRegistry(
			payment.NewWoovi(),
			payment.NewHeleket(),
			payment.NewManualPIX(),
			payment.NewManualUSDT(),
			payment.NewManualCrypto(),
			payment.NewStripe(cfg.SiteURL),
		)
		logger.Info("payments: legacy (providers in-memory)")
	}

	planSvc := application.NewPlanService(planRepo)
	currencySvc := application.NewCurrencyService(currencyRepo, planRepo)
	creditSvc := application.NewCreditService(creditRepo)
	profileSvc := application.NewProfileService(profileRepo)
	invoiceSvc := application.NewInvoiceService(invoiceRepo, gwRepo, userRepo, creditSvc, currencySvc, payments)
	checkoutSvc := application.NewCheckoutService(userRepo, planRepo, orderRepo, gwRepo, profileRepo, currencySvc, creditSvc, emailSender, payments, cfg.SiteURL)
	couponSvc := application.NewCouponService(couponRepo)
	checkoutSvc.SetCoupons(couponSvc)
	orderSvc := application.NewOrderService(orderRepo, planRepo)
	userNotifSvc := application.NewUserNotifService(db)
	userDataSvc := application.NewUserDataService(db)
	referralSvc := application.NewReferralService(referralRepo, userRepo, creditSvc)
	abSvc := application.NewABTestService(abRepo)
	fraudSvc := application.NewFraudService(db)
	refundSvc := application.NewRefundService(db, creditSvc)
	fraudCron := application.NewFraudVelocityCron(db)
	fraudCron.Start(context.Background())

	// Wave 2 hooks parciais (CheckoutService): plug Referral + Fraud.
	// PaymentReceiver e UserAuthService.SetReferrals são feitos depois, após
	// suas construções.
	checkoutSvc.SetReferrals(referralSvc)
	checkoutSvc.SetFraud(fraudSvc)
	// Tax setado depois quando taxSvc já existir (Wave 3).

	// Wave 3 services
	subscriptionSvc := application.NewSubscriptionService(subscriptionRepo, checkoutSvc)
	subscriptionSvc.SetUsers(userRepo)
	subscriptionSvc.SetPlans(planRepo)
	subscriptionSvc.SetProfiles(profileRepo)
	subscriptionCron := application.NewSubscriptionCron(subscriptionSvc)
	subscriptionCron.Start(context.Background())
	taxSvc := application.NewTaxService(taxRateRepo)
	checkoutSvc.SetTax(taxSvc)
	waSender := application.NewDryRunWhatsAppSender()
	waSvc := application.NewWhatsAppService(db, waSender)
	vendorSvc := application.NewVendorService(vendorRepo)
	apiKeySvc := application.NewAPIKeyService(apiKeyRepo)
	userEventSvc := application.NewUserEventService(userEventRepo)
	gwSvc := application.NewGatewayService(gwRepo)
	// JWT RS256 — carrega ou gera RSA privada. Tokens novos signam RS256;
	// ValidateAdmin/ValidateUser ainda aceitam HS256 antigos por compat (7d).
	rsaKey, err := jwtkeys.LoadOrGenerate(cfg.JWTPrivateKeyPath)
	if err != nil {
		log.Fatal(err)
	}
	authSvc := application.NewAuthService(adminRepo, roleRepo, rsaKey, []byte(cfg.JWTSecret), cfg.JWTTTL)
	userAuthSvc := application.NewUserAuthService(userRepo, rsaKey, []byte(cfg.JWTSecret), cfg.JWTTTL)
	// HS256 kill-switch (Fase 4.1 follow-up). Após janela de 7d, operador
	// seta LEGACY_HS256_DISABLED=true e tokens HS256 antigos param de ser
	// aceitos pelo ValidateAdmin/ValidateUser.
	authSvc.SetLegacyHS256Disabled(cfg.LegacyHS256Disabled)
	userAuthSvc.SetLegacyHS256Disabled(cfg.LegacyHS256Disabled)

	// 2FA — quando TWOFA_ENCRYPTION_KEY ausente, services ficam nil e os
	// endpoints retornam 503 + logins não bloqueiam (HML/dev). Em prod
	// o instalador gera a key na 1ª install + persiste em /etc/viralefy/.env.
	var adminTwoFA, userTwoFA *application.TwoFAService
	if len(cfg.TwoFAEncryptionKey) == 32 {
		adminTwoFA = application.NewTwoFAService(postgres.NewAdminTwoFARepo(db), cfg.TwoFAEncryptionKey)
		userTwoFA = application.NewTwoFAService(postgres.NewUserTwoFARepo(db), cfg.TwoFAEncryptionKey)
		authSvc.SetTwoFA(adminTwoFA)
		userAuthSvc.SetTwoFA(userTwoFA)
		logger.Info("2FA enabled (admin + user)")
	} else {
		logger.Warn("2FA disabled — TWOFA_ENCRYPTION_KEY missing or invalid (expected 32 bytes)")
	}
	ticketSvc := application.NewTicketService(ticketRepo, userRepo, emailSender, cfg.SiteURL)
	notifier := notify.NewWebhookClient(cfg.AdminWebhookURL)
	if !notifier.Enabled() {
		logger.Warn("admin webhook disabled (ADMIN_WEBHOOK_URL empty)")
	}
	paymentReceiver := application.NewPaymentReceiver(
		invoiceRepo, orderRepo, planRepo, userRepo,
		ticketSvc, invoiceSvc, emailSender, notifier, cfg.SiteURL,
	)
	// Wave 2 hooks restantes (Referral payout no PaymentReceiver + Referral
	// signup no UserAuthService.Register).
	paymentReceiver.SetReferrals(referralSvc)
	userAuthSvc.SetReferrals(referralSvc)

	// PHASE-8 Wave 3 hook: TelegramNotifier no PaymentReceiver. Quando o
	// sender remoto está plugado (senderRemote != nil), reaproveitamos como
	// TelegramNotifier — o senderclient.Client tem SendTelegram(handle,
	// template, vars). Sem sender remoto, o PaymentReceiver simplesmente
	// não dispara telegram (modo legacy não tem canal Telegram nativo).
	if senderRemote != nil {
		paymentReceiver.SetTelegram(senderRemote, cfg.TelegramAdminChatID)
		if cfg.TelegramAdminChatID == "" {
			logger.Warn("telegram admin chat not configured (TELEGRAM_ADMIN_CHAT_ID empty) — só notificação cliente roda")
		}
	}

	auditRepo := postgres.NewAuditRepo(db)
	auditSvc := application.NewAuditService(auditRepo)

	turnstileSvc := turnstile.NewService(cfg.TurnstileSecretKey)
	if !turnstileSvc.Enabled() {
		logger.Warn("turnstile disabled (TURNSTILE_SECRET_KEY empty) — anti-bot bypass")
	}

	metricCaptureSvc := application.NewMetricCaptureService(orderRepo, planRepo, profileRepo)
	// Plumba pro CheckoutService disparar baseline async no momento da
	// criação do pedido. Ver checkout_service.SetMetricCapture.
	checkoutSvc.SetMetricCapture(metricCaptureSvc)

	// Reviews — coleta pós-entrega + JSON-LD aggregateRating.
	reviewRepo := postgres.NewReviewRepo(db)
	reviewRequestRepo := postgres.NewReviewRequestRepo(db)
	reviewSvc := application.NewReviewService(reviewRepo, orderRepo, planRepo)

	// Cron de delivery capture: 24h pós-pago, tira snapshot da 2ª fonte de
	// verdade (perfil/post público) e grava em orders.delivery_metrics.
	// Substitui o fluxo manual de admin clicar "Capturar delivery agora" em
	// cada pedido. Intervalo 15min, batch 25 — config padrão tunada pra HML.
	deliveryCron := &application.DeliveryCaptureCron{
		Orders:  orderRepo,
		Metrics: metricCaptureSvc,
	}
	deliveryCron.Start(context.Background())

	// Cron de review request: 7d pós-pago, envia email "how was your order?"
	// com link pra /orders/{id}/review. Alimenta aggregateRating no JSON-LD
	// das páginas de plano (rich result + social proof real, sem fake).
	reviewCron := &application.ReviewRequestCron{
		Repo:    reviewRequestRepo,
		Email:   emailSender,
		SiteURL: cfg.SiteURL,
	}
	reviewCron.Start(context.Background())

	// Cron de cleanup idempotency_keys (TTL 24h). Resolve a tech debt do
	// middleware de idempotência — sem cleanup, a tabela cresce indefinido.
	idemCleanup := &application.IdempotencyCleanupCron{DB: db}
	idemCleanup.Start(context.Background())

	// Cron de retenção pra tabelas append-only de eventos (user_events,
	// ab_events, email_events). MaxAge=90d cobre look-back de Meta CAPI (28d)
	// + cohort analysis. user_journeys agregado fica intacto pra remarketing.
	eventRetention := &application.EventRetentionCron{DB: db}
	eventRetention.Start(context.Background())

	// Cron de drift em plan_prices: alerta se algum row fugir da fórmula
	// USD/100 * rate. Defensiva contra a regressão 2026-06-06 (rate change
	// sem cascade) voltar.
	priceDrift := &application.PlanPriceDriftCron{DB: db}
	priceDrift.Start(context.Background())

	// Email reputation — alimentado por POST /v1/webhooks/resend.
	emailRepuSvc := application.NewEmailReputationService(db)

	// Cart abandonment: cron que pega orders pending 1-24h com payment_url
	// e envia email "complete your purchase". Best-effort (errors warn).
	cartAbandonCron := application.NewCartAbandonmentCron(db, emailSender, cfg.SiteURL)
	cartAbandonCron.Start(context.Background())

	// Stripe reconcile: polling de orders pending Stripe há > 10min, no caso
	// do webhook não chegar (rede, retry esgotado, edge bloqueado). Tick 5min,
	// batch 50 — chamadas à Stripe API são cheap (GET /sessions/{id}) e
	// idempotentes; ConfirmByExternalRef faz no-op se já paid.
	stripeReconcile := &application.StripeReconcileCron{
		DB:       db,
		Receiver: paymentReceiver,
	}
	stripeReconcile.Start(context.Background())

	h := &httphandler.Handlers{
		Plans:           planSvc,
		Checkout:        checkoutSvc,
		Gateways:        gwSvc,
		Auth:            authSvc,
		UserAuth:        userAuthSvc,
		Currencies:      currencySvc,
		Categories:      categoryRepo,
		Orders:          orderRepo,
		Users:           userRepo,
		Tickets:         ticketSvc,
		Profiles:        profileSvc,
		Credits:         creditSvc,
		Invoices:        invoiceSvc,
		PaymentReceiver: paymentReceiver,
		Turnstile:       turnstileSvc,
		Audit:           auditSvc,
		DB:              db,
		Metrics:         metricCaptureSvc,
		Reviews:         reviewSvc,
		EmailRepu:       emailRepuSvc,
		Coupons:         couponSvc,
		OrderSvc:        orderSvc,
		Notifs:          userNotifSvc,
		UserData:        userDataSvc,
		CountryPPP:      countryPPPRepo,
		Referrals:       referralSvc,
		ABTests:         abSvc,
		Fraud:           fraudSvc,
		Refunds:         refundSvc,
		Subscriptions:   subscriptionSvc,
		TaxRates:        taxRateRepo,
		Tax:             taxSvc,
		WhatsApp:        waSvc,
		Vendors:         vendorSvc,
		APIKeys:         apiKeySvc,
		Events:          userEventSvc,
		Email:           emailSender,
		Storage:         buildStorage(cfg.Storage, logger),
		AdminTwoFA:      adminTwoFA,
		UserTwoFA:       userTwoFA,
		// PHASE-8 Wave 3: quando o paymentsRemote está plugado, a rota
		// /v1/plans/{id}/payment-methods proxy direto pro microserviço.
		// Nil = legacy CheckoutService.ListPaymentMethods.
		MethodsRemote: paymentsRemote,
	}

	// /ready faz Ping no pool — falha vira 503 (drena tráfego no rolling update).
	readyCheck := httphandler.ReadyChecker(func(r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		return db.Pool().Ping(ctx)
	})

	router := httphandler.NewRouter(h, cfg.CORSOrigins, readyCheck,
		httphandler.AdminAuth(authSvc),
		httphandler.UserAuth(userAuthSvc),
		httphandler.OptionalUserAuth(userAuthSvc),
		cfg.InternalSharedSecret,
	)
	addr := cfg.BindHost + ":" + cfg.Port
	srv := &http.Server{Addr: addr, Handler: router}

	go func() {
		logger.Info("viralefy_core listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("listen failed", "error", err.Error())
			log.Fatal(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = shutdownTracer(shutdownCtx)
}

// buildStorage instancia o cliente S3 quando config tem endpoint+credenciais.
// Falha em conectar NÃO derruba o bootstrap — substituímos por NoopStorage
// e o handler de upload cai no fluxo legacy base64. Permite o API subir em
// ambientes sem MinIO/R2 cadastrados ainda (HML inicial, tests).
func buildStorage(cfg config.StorageConfig, logger *slog.Logger) application.ObjectStorage {
	if !cfg.Enabled() {
		logger.Warn("object storage disabled (Storage.Endpoint empty) — proofs cair no fluxo base64 legacy")
		return application.NoopStorage{}
	}
	cli, err := storage.New(cfg)
	if err != nil {
		logger.Warn("object storage init failed — fallback to NoopStorage",
			"endpoint", cfg.Endpoint, "error", err.Error())
		return application.NoopStorage{}
	}
	logger.Info("object storage ready",
		"endpoint", cfg.Endpoint, "bucket_proofs", cfg.BucketProofs)
	return cli
}
