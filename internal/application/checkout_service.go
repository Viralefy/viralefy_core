package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
	"golang.org/x/crypto/bcrypt"
)

type CheckoutService struct {
	users      domain.UserRepository
	plans      domain.PlanRepository
	orders     domain.OrderRepository
	gateways   domain.GatewayRepository
	profiles   domain.ProfileRepository
	currencies *CurrencyService
	credits    *CreditService
	email      EmailSender
	payments   *PaymentRegistry
	siteURL    string
	// metrics é opcional. Quando setado via SetMetricCapture, cada Order.
	// Create dispara um snapshot best-effort em goroutine separada —
	// usado como segunda fonte de verdade ao verificar entrega do gateway.
	metrics *MetricCaptureService
	// coupons é opcional. Quando setado via SetCoupons, in.CouponCode é
	// validado e o desconto aplicado em AmountCents antes do Quote.
	coupons *CouponService
	// referrals é opcional. Quando setado, hook de signup grava o referral
	// se tracking traz referrer_code.
	referrals *ReferralService
	// fraud é opcional. Quando setado, IsBlocked(email|ip) é checado antes
	// de qualquer trabalho — pedidos suspeitos rejeitados como Forbidden.
	fraud *FraudService
	// tax é opcional. Quando setado E in.Country é EU/GB, VAT é adicionado
	// ao amount cobrado.
	tax *TaxService
}

// SetTax opt-in pra cobrança de VAT no settlement_amount.
func (s *CheckoutService) SetTax(svc *TaxService) {
	s.tax = svc
}

// SetMetricCapture liga o capture pós-criação. Chamado uma vez no
// bootstrap (main.go). Mantemos opcional pra testes não exigirem o
// scraper.
func (s *CheckoutService) SetMetricCapture(svc *MetricCaptureService) {
	s.metrics = svc
}

// fireBaselineCapture roda em goroutine separada (não bloqueia o
// checkout). Erros ficam no log; baseline_source vira "manual_pending"
// quando o scrape falha pro operador resolver.
func (s *CheckoutService) fireBaselineCapture(orderID string) {
	if s.metrics == nil {
		return
	}
	go func() {
		// context background — o request já voltou; queremos sobreviver
		// ao cancel do request. Timeout interno no MetricCaptureService.
		bg := context.Background()
		_ = s.metrics.CaptureBaseline(bg, orderID)
	}()
}

func NewCheckoutService(
	users domain.UserRepository,
	plans domain.PlanRepository,
	orders domain.OrderRepository,
	gateways domain.GatewayRepository,
	profiles domain.ProfileRepository,
	currencies *CurrencyService,
	credits *CreditService,
	email EmailSender,
	payments *PaymentRegistry,
	siteURL string,
) *CheckoutService {
	return &CheckoutService{
		users: users, plans: plans, orders: orders, gateways: gateways,
		profiles: profiles, currencies: currencies, credits: credits,
		email: email, payments: payments, siteURL: siteURL,
	}
}

// SetCoupons injeta o CouponService — opt-in pra não exigir o construtor
// em todos os testes que ainda não usam cupom.
func (s *CheckoutService) SetCoupons(svc *CouponService) {
	s.coupons = svc
}

// SetReferrals opt-in pra hook de signup (RecordReferral quando user é
// criado e tracking traz referrer_code).
func (s *CheckoutService) SetReferrals(svc *ReferralService) {
	s.referrals = svc
}

// SetFraud opt-in pra check pré-checkout (block por IsBlocked).
func (s *CheckoutService) SetFraud(svc *FraudService) {
	s.fraud = svc
}

type CheckoutInput struct {
	PlanID          string
	Email           string
	Name            string
	DisplayCurrency string
	// Alvo do serviço — um dos dois conforme plan.target_type:
	ProfileID      string // se target_type == profile (perfil já cadastrado)
	NewProfile     *NewProfileInline
	PublicationURL string // se target_type == publication
	// CustomData carrega o snapshot do formulário customizado da categoria
	// (Account Recovery, BMs, perfis). Schema livre; é guardado na order
	// e replayed no ticket aberto após pagamento.
	CustomData     map[string]any
	// Tracking (UTM/fbclid/gclid/referrer/landing_url/ip/user_agent).
	// Guardado na order e replicado em users.tracking_data se o user é
	// recém-criado nesse checkout (first-touch attribution).
	Tracking       map[string]any
	// Pagamento:
	PaymentMethod string // "gateway" (default) ou "credits". credits exige usuário logado com saldo
	UserID        string // setado pelo handler quando usuário está logado; obrigatório p/ credits
	// CouponCode opcional. Validado contra CouponService (vide SetCoupons);
	// erro nas regras → ErrInvalidInput (front mostra mensagem). Cupom
	// inexistente ou inelegível: rejeita o checkout inteiro pra evitar
	// surpresa de "comprou achando que tinha desconto".
	CouponCode string
	// Country opcional (ISO alpha-2 lowercase). Quando informado e o
	// TaxService está plugado, VAT é computado e adicionado ao amount.
	// Front detecta via /api/geo + localStorage.
	Country string
	// TargetCountry é o mercado da entrega — herdado da LP /[country]/.
	// /us/instagram-followers → TargetCountry="us". Operador usa pra
	// escolher supplier correto (seguidor americano vs alemão).
	TargetCountry string
	// GatewayID opcional. Quando fornecido, força o uso desse gateway
	// (escolhido pelo cliente no novo fluxo de seleção). Quando vazio,
	// cai no pickGateway por settlement (back-compat com checkout antigo).
	// Validamos que o gateway existe, está ativo e aceita a settlement
	// currency derivada do plano + display_currency.
	GatewayID string
	// PayCurrency opcional — moeda em que o cliente quer EFETIVAMENTE
	// pagar. Usado pra providers multi-currency (Heleket/Stripe) onde 1
	// gateway expõe N opções (BTC/ETH/USDT/LTC). Quando presente E o
	// gateway aceita, o charge é criado nessa moeda em vez do settlement
	// canonical. Heleket converte pra USDT internamente; Stripe cria
	// invoice na fiat escolhida.
	PayCurrency string
}

// NewProfileInline permite o usuário criar um perfil "no ato" do checkout
// (sem precisar ir antes em /account/profiles).
type NewProfileInline struct {
	Platform    string
	Handle      string
	DisplayName string
}

type CheckoutResult struct {
	OrderID            string             `json:"order_id"`
	Status             domain.OrderStatus `json:"status"`
	PlanName           string             `json:"plan_name"`
	DisplayCurrency    string             `json:"display_currency"`
	DisplaySymbol      string             `json:"display_symbol"`
	DisplayAmount      string             `json:"display_amount"`
	SettlementCurrency string             `json:"settlement_currency"`
	SettlementSymbol   string             `json:"settlement_symbol"`
	SettlementAmount   string             `json:"settlement_amount"`
	AccountCreated     bool               `json:"account_created"`
	Email              string             `json:"email"`
	EmailSent          bool               `json:"email_sent"`
	GatewayProvider    string             `json:"gateway_provider,omitempty"`
	PaymentURL         string             `json:"payment_url,omitempty"`
	PaymentExtra       map[string]string  `json:"payment_extra,omitempty"`
	PaymentMethod      string             `json:"payment_method"` // gateway | credits
	CreditsUsedCents   int                `json:"credits_used_cents,omitempty"`
	CreditBalanceCents int64              `json:"credit_balance_cents,omitempty"`
	// Quando cupom aplicado: preço original, desconto e final em USD-cents
	// (informativo pro front mostrar "$X off com BLACK10").
	CouponCode         string             `json:"coupon_code,omitempty"`
	OriginalUSDCents   int                `json:"original_usd_cents,omitempty"`
	DiscountUSDCents   int                `json:"discount_usd_cents,omitempty"`
}

func (s *CheckoutService) Checkout(ctx context.Context, in CheckoutInput) (*CheckoutResult, error) {
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" || in.Name == "" || in.PlanID == "" {
		return nil, domain.ErrInvalidInput
	}
	if in.PaymentMethod == "" {
		in.PaymentMethod = "gateway"
	}
	if in.PaymentMethod != "gateway" && in.PaymentMethod != "credits" {
		return nil, domain.ErrInvalidInput
	}

	// Fraud check pré-tudo. Email/IP em blocklist → 403 fast-fail (não toca
	// no plano nem cria order pending). IP deduzido da tracking (handler já
	// enriqueceu); fallback string vazia que IsBlocked trata como no-op.
	if s.fraud != nil {
		if blocked, _ := s.fraud.IsBlocked(ctx, in.Email); blocked {
			return nil, domain.ErrForbidden
		}
		if ipRaw, ok := in.Tracking["ip"]; ok {
			if ip, _ := ipRaw.(string); ip != "" {
				if blocked, _ := s.fraud.IsBlocked(ctx, ip); blocked {
					return nil, domain.ErrForbidden
				}
			}
		}
	}

	plan, err := s.plans.GetByID(ctx, in.PlanID)
	if err != nil {
		return nil, err
	}
	if !plan.Active {
		return nil, domain.ErrInvalidInput
	}

	// Resolve o alvo (perfil ou URL) e VALIDA contra o tipo do plano —
	// primeira defesa contra "mandar serviço errado".
	profileID, publicationURL, err := s.resolveTarget(ctx, plan, in)
	if err != nil {
		return nil, err
	}

	// Cupom (opcional). Valida + calcula desconto ANTES do Quote pra que
	// o display/settlement amount já reflitam o valor final cobrado.
	amountCents := plan.PriceCents
	var couponDiscountUSDCents int
	var couponCodeApplied string
	if in.CouponCode != "" && s.coupons != nil {
		preview, err := s.coupons.Preview(ctx, PreviewInput{
			Code:           in.CouponCode,
			AmountUSDCents: plan.PriceCents,
			PlanCategory:   plan.Category,
			UserEmail:      in.Email,
		})
		if err != nil {
			// Erro do cupom é InvalidInput pro cliente; mensagem específica
			// vai pelo error path do writeError (handler lê o erro).
			return nil, domain.ErrInvalidInput
		}
		amountCents = preview.FinalUSDCents
		couponDiscountUSDCents = preview.DiscountUSDCents
		couponCodeApplied = preview.Code
	}

	// Tax (Fase 5.3) — VAT EU/GB computado sobre o net (price - discount).
	// Adicionado ao amountCents ANTES do Quote. País fora do catálogo →
	// no-op silencioso (taxUSDCents=0, ratePct=0).
	var taxUSDCents int
	var taxRatePct float64
	if s.tax != nil && in.Country != "" {
		taxUSDCents, taxRatePct, _ = s.tax.ComputeTax(ctx, in.Country, amountCents)
		amountCents += taxUSDCents
	}

	quote, err := s.currencies.QuoteForPlan(ctx, plan.Prices, amountCents, in.DisplayCurrency)
	if err != nil {
		return nil, err
	}

	// Autocadastro: cria conta se não existir e gera senha; senão reaproveita.
	existing, _ := s.users.GetByEmail(ctx, in.Email)
	var userID, generatedPassword string
	accountCreated := false
	if existing != nil {
		userID = existing.ID
	} else {
		if in.PaymentMethod == "credits" {
			// Não dá pra pagar com créditos sem ter conta.
			return nil, domain.ErrUnauthorized
		}
		generatedPassword = GeneratePassword()
		hash, err := bcrypt.GenerateFromPassword([]byte(generatedPassword), 12)
		if err != nil {
			return nil, err
		}
		userID = uuid.New().String()
		if err := s.users.Create(ctx, domain.User{
			ID: userID, Email: in.Email, Name: in.Name, Instagram: "",
			PasswordHash: string(hash),
			// First-touch attribution: o checkout anônimo cria o user e já
			// guarda o tracking — fica disponível pra CAPI/Events API.
			TrackingData: in.Tracking,
		}); err != nil {
			return nil, err
		}
		accountCreated = true
		// Referral signup hook. Idempotente — RecordReferral só seta se ainda
		// não tem referred_by. Falhas são best-effort (não derrubam checkout).
		if s.referrals != nil {
			if rc, ok := in.Tracking["referrer_code"].(string); ok && rc != "" {
				_ = s.referrals.RecordReferral(ctx, userID, rc)
			}
		}
	}
	if in.UserID != "" && in.UserID != userID {
		// se o token de usuário diz outra coisa, força o user do token (segurança)
		userID = in.UserID
	}

	// Se o handler quer criar um perfil inline pro usuário logado:
	if in.NewProfile != nil && profileID == "" && plan.TargetType == "profile" {
		platform := domain.Platform(in.NewProfile.Platform)
		if err := ValidateHandle(platform, in.NewProfile.Handle); err != nil {
			return nil, domain.ErrInvalidInput
		}
		np := domain.Profile{
			ID:          uuid.New().String(),
			UserID:      userID,
			Platform:    platform,
			Handle:      NormalizeHandle(in.NewProfile.Handle),
			DisplayName: in.NewProfile.DisplayName,
			Verified:    true,
		}
		if err := s.profiles.Create(ctx, np); err != nil {
			return nil, err
		}
		profileID = np.ID
	}

	orderID := uuid.New().String()
	order := domain.Order{
		ID:                 orderID,
		UserID:             userID,
		PlanID:             plan.ID,
		Status:             domain.OrderStatusPending,
		AmountCents:        amountCents, // já descontado + tax se cupom/VAT aplicou
		Currency:           plan.Currency,
		TaxCountryCode:     in.Country,
		TaxRatePct:         taxRatePct,
		TaxUSDCents:        taxUSDCents,
		TargetCountryCode:  in.TargetCountry,
		DisplayCurrency:    quote.DisplayCurrency,
		DisplayAmount:      quote.DisplayAmount,
		SettlementCurrency: quote.SettlementCurrency,
		SettlementAmount:   quote.SettlementAmount,
		PaymentMethod:      in.PaymentMethod,
		CustomData:         in.CustomData,
		Tracking:           in.Tracking,
	}
	if profileID != "" {
		order.ProfileID = &profileID
	}
	if publicationURL != "" {
		order.PublicationURL = &publicationURL
	}

	// ---------- Caminho A: pagamento com créditos ----------
	if in.PaymentMethod == "credits" {
		// Conta + saldo.
		acct, err := s.credits.Balance(ctx, userID)
		if err != nil {
			return nil, err
		}
		if acct.BalanceCents < int64(amountCents) {
			return nil, domain.ErrInvalidInput // saldo insuficiente — front trata
		}
		// Pedido já entra como pago (não há cobrança externa).
		order.Status = domain.OrderStatusPaid
		order.CreditsUsedCents = amountCents
		if err := s.orders.Create(ctx, order); err != nil {
			return nil, err
		}
		s.redeemCoupon(ctx, couponCodeApplied, orderID, in.Email, couponDiscountUSDCents)
		s.fireBaselineCapture(orderID)
		// Debita do ledger (atômico no repo).
		newAcct, err := s.credits.Spend(ctx, userID, int64(amountCents), "Pedido "+plan.Name, &orderID)
		if err != nil {
			return nil, err
		}
		emailSent := s.sendCheckoutEmail(ctx, in.Email, in.Name, *plan, quote, nil, "", nil, generatedPassword, accountCreated, true)

		return &CheckoutResult{
			OrderID: orderID, Status: order.Status, PlanName: plan.Name,
			DisplayCurrency: quote.DisplayCurrency, DisplaySymbol: quote.DisplaySymbol, DisplayAmount: quote.DisplayAmount,
			SettlementCurrency: quote.SettlementCurrency, SettlementSymbol: quote.SettlementSymbol, SettlementAmount: quote.SettlementAmount,
			AccountCreated: accountCreated, Email: in.Email, EmailSent: emailSent,
			PaymentMethod: "credits", CreditsUsedCents: amountCents,
			CreditBalanceCents: newAcct.BalanceCents,
			CouponCode: couponCodeApplied, OriginalUSDCents: plan.PriceCents, DiscountUSDCents: couponDiscountUSDCents,
		}, nil
	}

	// ---------- Caminho B: pagamento via gateway (padrão) ----------
	// Fluxo novo: cliente pode passar GatewayID escolhido na UI de seleção
	// de métodos. Validamos que o gateway está ativo e aceita a settlement
	// currency. Fallback: pickGateway por settlement (back-compat).
	// Pra providers multi-currency (Stripe/Heleket), o cliente escolheu
	// uma moeda específica de pay-in. Passamos essa como "acceptedCurrency"
	// alvo da validação — Stripe aceita USD/EUR/BRL/GBP mas NÃO USDT (o
	// settlement canônico). Sem esse fallback, todo checkout Stripe quebra
	// com 422 porque o resolveGateway nunca achava match contra USDT.
	wantedCurrency := quote.SettlementCurrency
	if in.PayCurrency != "" {
		wantedCurrency = strings.ToUpper(strings.TrimSpace(in.PayCurrency))
	}
	gw := s.resolveGateway(ctx, in.GatewayID, wantedCurrency, in.Country)
	// Pagamento via gateway exige UM gateway válido. Sem ele, recusa o
	// checkout antes de criar a order — evita pedido pending órfão e
	// elimina a porta de entrada que deixava PIX vazar pra internacional
	// via pickGateway/default-active.
	if gw == nil {
		return nil, domain.ErrInvalidInput
	}
	order.GatewayID = &gw.ID
	if err := s.orders.Create(ctx, order); err != nil {
		return nil, err
	}
	s.redeemCoupon(ctx, couponCodeApplied, orderID, in.Email, couponDiscountUSDCents)
	s.fireBaselineCapture(orderID)

	provider := gw.Provider
	var paymentURL string
	var paymentExtra map[string]string
	// Pra providers multi-currency (Heleket/Stripe) o cliente escolheu
	// uma moeda específica pra pagar. Calculamos o amount nessa moeda e
	// usamos no charge — Heleket cria invoice em BTC, Stripe sessão em
	// EUR, etc. Pra providers single-currency (PIX/manual_crypto) o
	// payCurrency é ignorado.
	chargeAmount := quote.SettlementAmount
	chargeCurrency := quote.SettlementCurrency
	if in.PayCurrency != "" && multiCurrencyProviders[strings.ToLower(strings.TrimSpace(gw.Provider))] {
		if amount, code, ok := s.amountInCurrency(ctx, plan, in.PayCurrency); ok && gwAccepts(gw, code) {
			chargeAmount = amount
			chargeCurrency = code
		}
	}
	if p, ok := s.payments.Get(gw.Provider); ok {
		charge, perr := p.CreateCharge(ctx, PaymentChargeInput{
			OrderID:     orderID,
			GatewayID:   gw.ID,
			Provider:    gw.Provider,
			Description: plan.Name,
			Amount:      chargeAmount,
			Currency:    chargeCurrency,
			Customer:    PaymentCustomer{Name: in.Name, Email: in.Email},
			Config:      gw.Config,
		})
		if perr != nil {
			observability.FromContext(ctx).Warn("checkout: payment provider failed",
				"provider", gw.Provider,
				"error", perr.Error(),
			)
		} else {
			paymentURL = charge.PaymentURL
			paymentExtra = charge.Extra
			_ = s.orders.UpdatePayment(ctx, orderID, charge.ExternalRef, charge.PaymentURL, charge.Extra)
		}
	}

	emailSent := s.sendCheckoutEmail(ctx, in.Email, in.Name, *plan, quote, gw, paymentURL, paymentExtra, generatedPassword, accountCreated, false)

	return &CheckoutResult{
		OrderID: orderID, Status: domain.OrderStatusPending, PlanName: plan.Name,
		DisplayCurrency: quote.DisplayCurrency, DisplaySymbol: quote.DisplaySymbol, DisplayAmount: quote.DisplayAmount,
		SettlementCurrency: quote.SettlementCurrency, SettlementSymbol: quote.SettlementSymbol, SettlementAmount: quote.SettlementAmount,
		AccountCreated: accountCreated, Email: in.Email, EmailSent: emailSent,
		GatewayProvider: provider, PaymentURL: paymentURL, PaymentExtra: paymentExtra,
		PaymentMethod:   "gateway",
		CouponCode:      couponCodeApplied,
		OriginalUSDCents: plan.PriceCents,
		DiscountUSDCents: couponDiscountUSDCents,
	}, nil
}

// redeemCoupon registra o uso após o order ser criado. Best-effort: erros
// não derrubam o checkout (o ticket já foi cobrado e gravado). Drift
// eventual aparece no audit log se houver problema.
func (s *CheckoutService) redeemCoupon(ctx context.Context, code, orderID, email string, discountUSDCents int) {
	if code == "" || s.coupons == nil || discountUSDCents <= 0 {
		return
	}
	if err := s.coupons.Redeem(ctx, RedeemInput{
		Code: code, OrderID: orderID, UserEmail: email, DiscountUSDCents: discountUSDCents,
	}); err != nil {
		observability.FromContext(ctx).Warn("coupon redeem failed (order ok)",
			"order_id", orderID, "coupon", code, "error", err.Error())
	}
}

// resolveTarget valida que o alvo informado bate com plan.target_type e
// retorna profileID/URL apropriados pra persistir no pedido.
func (s *CheckoutService) resolveTarget(ctx context.Context, plan *domain.Plan, in CheckoutInput) (string, string, error) {
	switch plan.TargetType {
	case "profile", "":
		// Aceita ProfileID existente OU NewProfile inline. Pelo menos um.
		if in.ProfileID != "" {
			p, err := s.profiles.GetByID(ctx, in.ProfileID)
			if err != nil {
				return "", "", domain.ErrInvalidInput
			}
			// Confere plataforma: serviço de TikTok não pode ir num perfil IG.
			if plan.Platform != "" && string(p.Platform) != plan.Platform {
				return "", "", domain.ErrInvalidInput
			}
			return p.ID, "", nil
		}
		if in.NewProfile != nil {
			// Plataforma do inline tem que casar com a do plano.
			if plan.Platform != "" && in.NewProfile.Platform != plan.Platform {
				return "", "", domain.ErrInvalidInput
			}
			return "", "", nil // será criado depois (precisamos do userID)
		}
		return "", "", domain.ErrInvalidInput
	case "publication":
		if in.PublicationURL == "" {
			return "", "", domain.ErrInvalidInput
		}
		platform := domain.Platform(plan.Platform)
		if platform == "" {
			platform = domain.PlatformInstagram
		}
		if err := ValidatePublicationURL(platform, in.PublicationURL); err != nil {
			return "", "", domain.ErrInvalidInput
		}
		return "", strings.TrimSpace(in.PublicationURL), nil
	}
	return "", "", domain.ErrInvalidInput
}

// resolveGateway aplica a regra do fluxo novo: se o cliente escolheu um
// gateway na UI de seleção, valida e usa esse; senão cai no pickGateway
// (back-compat). Gateway inválido (desativado, inexistente, ou que não
// aceita a settlement currency) → retorna nil. ZERO fallback silencioso.
// O caller decide se rejeita o checkout ou tenta outro caminho.
//
// Aplica também o brOnly filter de gatewayEligible: se cliente mandou
// gateway_id de PIX/Woovi mas country != "br", recusa. Sem essa camada,
// um cliente malicioso (ou bug de UI) consegue forçar PIX em compra
// internacional via id direto, e a desgraça reaparece.
func (s *CheckoutService) resolveGateway(ctx context.Context, gatewayID, settlement, country string) *domain.PaymentGateway {
	if gatewayID == "" {
		return s.pickGateway(ctx, settlement, country)
	}
	g, err := s.gateways.GetByID(ctx, gatewayID)
	if err != nil || g == nil || !g.Active {
		return nil
	}
	// PIX/Woovi: country DEVE ser br. Trava hard mesmo que admin tenha
	// configurado BRL na lista de accepted_currencies do gateway.
	if brOnlyProviders[strings.ToLower(strings.TrimSpace(g.Provider))] {
		if strings.ToLower(strings.TrimSpace(country)) != "br" {
			return nil
		}
		return g
	}
	// Garantia: gateway aceita a moeda. Sem isso o customer escolheria
	// "Stripe USD" pra um pedido em BRL e o charge quebraria.
	want := strings.ToUpper(strings.TrimSpace(settlement))
	for _, c := range g.AcceptedCurrencies {
		if strings.ToUpper(c) == want {
			return g
		}
	}
	return nil
}

// pickGateway escolhe o gateway adequado pra moeda de liquidação no fluxo
// legacy (sem gateway_id). Honra brOnly filter: PIX/Woovi só voltam pra
// country=br. Internacional cai direto em crypto provider; se nenhum
// estiver ativo, retorna nil pro caller rejeitar.
func (s *CheckoutService) pickGateway(ctx context.Context, settlement, country string) *domain.PaymentGateway {
	isBR := strings.ToLower(strings.TrimSpace(country)) == "br"
	candidate := ""
	switch strings.ToUpper(settlement) {
	case "BRL":
		if isBR {
			candidate = "woovi"
		}
	case "USDT", "BTC":
		candidate = "heleket"
	}
	if candidate != "" {
		if g, err := s.gateways.GetActiveByProvider(ctx, candidate); err == nil && g != nil {
			return g
		}
	}
	// Fallback: default active. Mas SE for BR-only e country não-br, recusa.
	g, _ := s.gateways.GetDefaultActive(ctx)
	if g != nil && !isBR && brOnlyProviders[strings.ToLower(strings.TrimSpace(g.Provider))] {
		return nil
	}
	return g
}

func (s *CheckoutService) sendCheckoutEmail(
	ctx context.Context, to, name string, plan domain.Plan, q Quote, gw *domain.PaymentGateway,
	paymentURL string, paymentExtra map[string]string,
	password string, accountCreated bool, paidWithCredits bool,
) bool {
	data := CheckoutEmailData{
		SiteURL:              s.siteURL,
		Name:                 name,
		Email:                to,
		PlanName:             plan.Name,
		DisplayCurrency:      q.DisplayCurrency,
		DisplaySymbol:        q.DisplaySymbol,
		DisplayAmount:        q.DisplayAmount,
		SettlementCurrency:   q.SettlementCurrency,
		SettlementAmount:     q.SettlementAmount,
		AccountCreated:       accountCreated,
		Password:             password,
		PaymentURL:           paymentURL,
		FallbackInstructions: "Payment instructions will follow shortly. If you have any questions, open a support ticket.",
	}
	if paidWithCredits {
		data.FallbackInstructions = "Credits payment confirmed. Your order is already in production."
	}
	if paymentExtra != nil {
		data.BrCode = paymentExtra["br_code"]
		data.QrImage = paymentExtra["qr_code_image"]
		data.CryptoAddress = paymentExtra["address"]
		data.CryptoNetwork = paymentExtra["network"]
	}
	if data.PixKey == "" && gw != nil && q.SettlementCurrency == "BRL" {
		data.PixKey = gw.Config["pix_key"]
	}

	subject, html, text, err := BuildCheckoutEmail(data)
	if err != nil {
		observability.FromContext(ctx).Error("checkout: render email failed", "error", err.Error())
		return false
	}
	if err := s.email.Send(ctx, EmailMessage{To: to, Subject: subject, TextBody: text, HTMLBody: html}); err != nil {
		observability.FromContext(ctx).Error("checkout: send email failed",
			"to_masked", observability.MaskEmail(to),
			"error", err.Error(),
		)
		return false
	}
	return true
}
