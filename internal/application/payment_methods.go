package application

import (
	"context"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// PaymentMethodOption descreve um método de pagamento DISPONÍVEL pra um
// pedido específico. O cliente vê uma lista desses cards no checkout e
// escolhe um. Cada opção carrega:
//   - GatewayID  — id a ser passado no POST /v1/checkout
//   - Kind       — card | pix | crypto_manual | crypto_auto (UI escolhe ícone)
//   - ChargedAmount/Currency — o que ele EFETIVAMENTE paga (ex.: R$50,00 BRL)
//   - SettlementAmount/Currency — o que cai na plataforma (ex.: 10.00 USDT)
//   - ConversionNote — string de transparência ("você paga R$50, a plataforma
//                      recebe 10 USDT após conversão"). Só populada quando
//                      Charged ≠ Settlement.
//   - NetworkLabel/NetworkWarning — pra crypto: "USDT (TRC20)" + aviso.
type PaymentMethodOption struct {
	GatewayID          string `json:"gateway_id"`
	Provider           string `json:"provider"`
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	ChargedCurrency    string `json:"charged_currency"`
	ChargedAmount      string `json:"charged_amount"`
	ChargedSymbol      string `json:"charged_symbol"`
	SettlementCurrency string `json:"settlement_currency"`
	SettlementAmount   string `json:"settlement_amount"`
	SettlementSymbol   string `json:"settlement_symbol"`
	DisplayCurrency    string `json:"display_currency"`
	DisplayAmount      string `json:"display_amount"`
	ConversionNote     string `json:"conversion_note,omitempty"`
	NetworkLabel       string `json:"network_label,omitempty"`
	NetworkWarning     string `json:"network_warning,omitempty"`
}

// ListPaymentMethods retorna os métodos de pagamento aceitos pra um plano,
// já com o preview de quanto o cliente vai pagar EM CADA gateway. Não cria
// pedido — é só o catálogo pra UI montar a lista de cards.
//
// Algoritmo:
//   1. resolve quote padrão (display + settlement por currency.settlement_code)
//   2. lista TODOS os gateways ativos
//   3. pra cada gateway, escolhe a currency natural dele:
//      - se aceita a settlement currency → usa
//      - senão usa a primeira da lista (ex.: PIX só aceita BRL)
//   4. computa charged_amount nessa currency usando amountFor
//   5. monta conversion_note quando charged ≠ settlement
//
// Filtros (futuros): por país (ex.: PIX só BR). Não bloqueamos hoje porque
// não conhecemos a heurística país→método sem mapping explícito; deixamos
// a UI esconder o que não fizer sentido (ex.: PIX se country != "br").
func (s *CheckoutService) ListPaymentMethods(
	ctx context.Context, planID, displayCurrency, country string,
) ([]PaymentMethodOption, error) {
	plan, err := s.plans.GetByID(ctx, planID)
	if err != nil {
		return nil, err
	}
	if !plan.Active {
		return nil, domain.ErrInvalidInput
	}
	quote, err := s.currencies.QuoteForPlan(ctx, plan.Prices, plan.PriceCents, displayCurrency)
	if err != nil {
		return nil, err
	}
	all, err := s.gateways.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PaymentMethodOption, 0, len(all))
	for _, g := range all {
		if !g.Active {
			continue
		}
		if !gatewayEligible(g, quote.DisplayCurrency, quote.SettlementCurrency, country) {
			continue
		}
		out = append(out, s.buildMethodOptions(ctx, g, plan, quote)...)
	}
	return out, nil
}

// multiCurrencyProviders são providers onde 1 gateway = N opções de pay-in
// (um card por accepted_currency). Heleket é um processor crypto que aceita
// vários assets de entrada (BTC/ETH/USDT/LTC) e converte automático na
// liquidação. Stripe pode rodar em múltiplas fiat (USD/EUR/BRL/GBP), cada
// uma vira sua própria opção.
//
// Providers fora desta lista renderizam UM card por gateway (manual_pix,
// manual_crypto — cada um já é uma rota única).
var multiCurrencyProviders = map[string]bool{
	"heleket": true,
	"stripe":  true,
}

// cryptoProviders são os providers que efetivamente cobram em crypto
// (USDT/BTC/etc.) — esses recebem o passe universal porque a conversão
// é resolvida internamente. Providers fiat (PIX/Stripe) NÃO recebem,
// mesmo se admin ticou USDT na lista de accepted_currencies por engano —
// PIX literalmente só cobra BRL on-rail; aceitar USDT lá seria mentira.
var cryptoProviders = map[string]bool{
	"manual_crypto": true,
	"manual_usdt":   true,
	"heleket":       true,
}

// brOnlyProviders — providers que SÓ fazem sentido pra cliente brasileiro.
// PIX é rail doméstico do Banco Central; um alemão em EUR ou americano em
// USD não tem como gerar PIX. Mostrar PIX pra eles é mentira que confunde
// (e foi a desgraça que veio assombrando 3 revisões da lógica de filtro).
//
// REGRA HARD: esses providers SÓ aparecem se country=="br". Display ou
// settlement currency em BRL NÃO é suficiente — currency é preferência
// de visualização, não nacionalidade. Sem country detectado → esconde.
var brOnlyProviders = map[string]bool{
	"manual_pix": true,
	"woovi":      true,
	"abacatepay": true,
}

// gatewayEligible decide se um gateway deve aparecer pro cliente. Regras
// em ordem de precedência:
//
//  1. brOnlyProviders (PIX/Woovi): country DEVE ser "br". Ponto.
//     Display=BRL pra alemão NÃO conta. Esconde se country vazio.
//  2. cryptoProviders com USDT: UNIVERSAL — qualquer display, conversão
//     resolvida via conversion_note.
//  3. Display ou settlement currency em accepted_currencies → mostra.
//  4. Qualquer outro caso → esconde.
//
// PIX é a regra mais sensível e a causa de bugs sucessivos: USDT marcado
// na lista por engano, fallback de pickGateway pegando o único ativo,
// display=BRL default de currency picker. Por isso bloqueio hard por
// provider — não dá pra cliente internacional pagar via PIX nem com toda
// boa vontade do mundo, então simplesmente não oferecemos a opção.
func gatewayEligible(g domain.PaymentGateway, displayCurrency, settlementCurrency, country string) bool {
	display := strings.ToUpper(strings.TrimSpace(displayCurrency))
	settle := strings.ToUpper(strings.TrimSpace(settlementCurrency))
	country = strings.ToLower(strings.TrimSpace(country))
	provider := strings.ToLower(strings.TrimSpace(g.Provider))

	// Regra 1: BR-only. PIX/Woovi → SÓ se country=br. Curto-circuita TUDO.
	if brOnlyProviders[provider] {
		return country == "br"
	}

	isCrypto := cryptoProviders[provider]
	for _, raw := range g.AcceptedCurrencies {
		c := strings.ToUpper(strings.TrimSpace(raw))
		// USDT universal SÓ pra crypto providers reais.
		if c == "USDT" && isCrypto {
			return true
		}
		if c == display || c == settle {
			return true
		}
	}
	return false
}

// buildMethodOptions expande um gateway em uma ou mais PaymentMethodOption.
//
// Providers em multiCurrencyProviders (Heleket, Stripe) emitem UM card por
// accepted_currency — o cliente vê "Heleket — pay in BTC", "Heleket — pay in
// USDT", "Heleket — pay in LTC" como opções distintas, cada uma com sua
// conversão e sua moeda de "final settlement" (USDT na maioria).
//
// Providers single-currency (manual_pix, manual_crypto — cada um já é UMA
// rota fixa de pagamento) emitem UM card por gateway.
//
// 2026-06-09: multi-currency providers (Stripe/Heleket) também passaram a
// emitir UM card por gateway. Decisão de produto — cliente quer um único
// método visível, a conversão pra moeda display vai em conversion_note.
// Regra de seleção da moeda primária: pickPrimaryCurrency().
func (s *CheckoutService) buildMethodOptions(
	ctx context.Context, g domain.PaymentGateway, plan *domain.Plan, quote Quote,
) []PaymentMethodOption {
	if len(g.AcceptedCurrencies) == 0 {
		return nil
	}
	code := pickPrimaryCurrency(g, quote.DisplayCurrency)
	if code == "" {
		return nil
	}
	if opt, ok := s.buildSingleOption(ctx, g, plan, quote, code); ok {
		return []PaymentMethodOption{opt}
	}
	return nil
}

// pickPrimaryCurrency espelha a versão em viralefy_payments/.../payment_methods.go.
// Mantemos duas implementações em sync porque o monolith ainda calcula a lista
// localmente quando não está em modo microservice.
//
//   - Heleket (crypto multi): prefere USDT (stable).
//   - Stripe (fiat multi): prefere display currency se aceita; senão USD; senão primeira.
//   - Single-currency: primeira moeda aceita.
func pickPrimaryCurrency(g domain.PaymentGateway, displayCurrency string) string {
	display := strings.ToUpper(strings.TrimSpace(displayCurrency))
	provider := strings.ToLower(strings.TrimSpace(g.Provider))
	accepted := make([]string, 0, len(g.AcceptedCurrencies))
	for _, raw := range g.AcceptedCurrencies {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if code != "" {
			accepted = append(accepted, code)
		}
	}
	if len(accepted) == 0 {
		return ""
	}
	contains := func(code string) bool {
		for _, c := range accepted {
			if c == code {
				return true
			}
		}
		return false
	}
	if !multiCurrencyProviders[provider] {
		return accepted[0]
	}
	if cryptoProviders[provider] && contains("USDT") {
		return "USDT"
	}
	if display != "" && contains(display) {
		return display
	}
	if contains("USD") {
		return "USD"
	}
	return accepted[0]
}

// buildSingleOption emite UM PaymentMethodOption pra (gateway, chargedCurrency).
// Centraliza a lógica de conversion_note + network warning (crypto).
func (s *CheckoutService) buildSingleOption(
	ctx context.Context, g domain.PaymentGateway, plan *domain.Plan, quote Quote, chargedCurrency string,
) (PaymentMethodOption, bool) {
	cur, err := s.currencies.repo.GetByCode(ctx, chargedCurrency)
	if err != nil || cur == nil {
		return PaymentMethodOption{}, false
	}
	chargedAmount := amountFor(plan.Prices, plan.PriceCents, *cur)
	// Settlement: prioriza o settlement_code da própria moeda (BTC/ETH/USDT
	// settle em USDT por config; EUR settle em EUR). Fallback: quote.Settlement.
	settleCurrency := cur.SettlementCode
	if settleCurrency == "" {
		settleCurrency = quote.SettlementCurrency
	}
	settleCurrency = strings.ToUpper(strings.TrimSpace(settleCurrency))
	settleAmount := chargedAmount
	settleSymbol := cur.Symbol
	if !strings.EqualFold(chargedCurrency, settleCurrency) {
		settleCur, err := s.currencies.repo.GetByCode(ctx, settleCurrency)
		if err == nil && settleCur != nil {
			settleAmount = amountFor(plan.Prices, plan.PriceCents, *settleCur)
			settleSymbol = settleCur.Symbol
		} else {
			settleAmount = quote.SettlementAmount
			settleSymbol = quote.SettlementSymbol
		}
	}
	// Agora que cada gateway emite UM card único, name é o label puro do
	// gateway. A moeda cobrada aparece em charged_amount/charged_currency +
	// conversion_note explica a diferença com display.
	name := g.Name
	opt := PaymentMethodOption{
		GatewayID:          g.ID,
		Provider:           g.Provider,
		Name:               name,
		Kind:               kindOf(g.Provider),
		ChargedCurrency:    chargedCurrency,
		ChargedAmount:      chargedAmount,
		ChargedSymbol:      cur.Symbol,
		SettlementCurrency: settleCurrency,
		SettlementAmount:   settleAmount,
		SettlementSymbol:   settleSymbol,
		DisplayCurrency:    quote.DisplayCurrency,
		DisplayAmount:      quote.DisplayAmount,
	}
	// Transparência: aviso quando display ≠ charged (cliente viu €50,
	// vai pagar X em BTC) OU quando charged ≠ settlement (paga em BTC,
	// plataforma recebe em USDT — cobra fee implícita do processor).
	if !strings.EqualFold(chargedCurrency, quote.DisplayCurrency) {
		opt.ConversionNote = "Price shown: " + quote.DisplaySymbol + " " + quote.DisplayAmount +
			" " + quote.DisplayCurrency + ". You pay " + cur.Symbol + " " + chargedAmount +
			" " + chargedCurrency + " — platform settles in " + settleCurrency +
			" (" + settleAmount + " " + settleCurrency + ")."
	} else if !strings.EqualFold(chargedCurrency, settleCurrency) {
		opt.ConversionNote = "You pay " + cur.Symbol + " " + chargedAmount + " " + chargedCurrency +
			"; platform receives " + settleAmount + " " + settleCurrency + " after auto-conversion."
	}
	if g.Provider == "manual_crypto" || g.Provider == "manual_usdt" {
		if net := strings.TrimSpace(g.Config["network"]); net != "" {
			opt.NetworkLabel = strings.TrimSpace(g.Config["network_label"])
			if opt.NetworkLabel == "" {
				opt.NetworkLabel = chargedCurrency + " (" + net + ")"
			}
			opt.NetworkWarning = strings.TrimSpace(g.Config["network_warning"])
			if opt.NetworkWarning == "" {
				opt.NetworkWarning = "Send ONLY on the " + net +
					" network. Deposits on any other network will be lost forever."
			}
		}
	}
	return opt, true
}

// amountInCurrency calcula o valor de um plano em uma moeda específica.
// Usado pelo checkout quando o cliente escolhe uma moeda de pay-in (Heleket
// multi-currency) — o charge precisa do amount já convertido pra essa moeda.
// Retorna (amount, code-normalized, ok). ok=false quando a moeda não está
// cadastrada no pool de currencies.
func (s *CheckoutService) amountInCurrency(ctx context.Context, plan *domain.Plan, currencyCode string) (string, string, bool) {
	code := strings.ToUpper(strings.TrimSpace(currencyCode))
	if code == "" {
		return "", "", false
	}
	cur, err := s.currencies.repo.GetByCode(ctx, code)
	if err != nil || cur == nil {
		return "", "", false
	}
	return amountFor(plan.Prices, plan.PriceCents, *cur), code, true
}

// gwAccepts verifica se um gateway tem currency code em sua lista de
// accepted_currencies. Case-insensitive, trim. Defense in depth: cliente
// pode mandar pay_currency arbitrário no payload; gateway só aceita o que
// admin cadastrou.
func gwAccepts(g *domain.PaymentGateway, code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range g.AcceptedCurrencies {
		if strings.ToUpper(strings.TrimSpace(c)) == code {
			return true
		}
	}
	return false
}

// pickChargedCurrency escolhe a moeda em que o gateway efetivamente cobra.
// Heurística:
//   - se aceita a settlement (USDT na maioria dos casos) → cobra em settlement
//   - senão pega a primeira da lista (ex.: Woovi/manual_pix só BRL)
// Evita decisão errada como mostrar "Pague R$50 em USDT" pra um gateway PIX.
func pickChargedCurrency(accepted []string, settlement string) string {
	settlement = strings.ToUpper(settlement)
	for _, c := range accepted {
		if strings.ToUpper(c) == settlement {
			return settlement
		}
	}
	return strings.ToUpper(strings.TrimSpace(accepted[0]))
}

// kindOf mapeia provider → kind genérico (UI usa pra ícone/etiqueta).
func kindOf(provider string) string {
	switch provider {
	case "woovi", "manual_pix", "abacatepay":
		return "pix"
	case "stripe":
		return "card"
	case "manual_crypto", "manual_usdt":
		return "crypto_manual"
	case "heleket":
		return "crypto_auto"
	}
	return "other"
}

