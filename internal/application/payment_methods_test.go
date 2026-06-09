package application

import (
	"testing"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// gateway tests sem DB: gatewayEligible é função pura sobre uma struct
// PaymentGateway + 3 strings. Permite cobrir o cruzamento provider × moeda
// × country sem instanciar repo/quote/checkout.

func gwFixture(provider string, accepted ...string) domain.PaymentGateway {
	return domain.PaymentGateway{
		ID:                 "gw-" + provider,
		Provider:           provider,
		Active:             true,
		AcceptedCurrencies: accepted,
	}
}

// O bug que veio assombrando 3 revisões: cliente alemão em EUR via
// PIX só porque o gateway tinha BRL na lista. A regra agora é provider-
// based hard. Estes testes lockam.

func TestGatewayEligible_PIX_hidesForNonBR_evenIfDisplayIsBRL(t *testing.T) {
	pix := gwFixture("manual_pix", "BRL")
	cases := []struct {
		name             string
		display, settle  string
		country          string
		wantEligible     bool
	}{
		{"alemão em EUR", "EUR", "EUR", "de", false},
		{"americano em USD", "USD", "USDT", "us", false},
		{"sem country detectado", "BRL", "BRL", "", false},
		{"display BRL é só preferência, não nacionalidade", "BRL", "BRL", "us", false},
		{"settlement em BRL pra estrangeiro", "USD", "BRL", "de", false},
		{"brasileiro em BRL", "BRL", "BRL", "br", true},
		{"brasileiro navegando em USD", "USD", "USDT", "br", true},
		{"brasileiro em EUR", "EUR", "EUR", "br", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gatewayEligible(pix, tc.display, tc.settle, tc.country)
			if got != tc.wantEligible {
				t.Fatalf("got %v, want %v (display=%s settle=%s country=%s)",
					got, tc.wantEligible, tc.display, tc.settle, tc.country)
			}
		})
	}
}

func TestGatewayEligible_Woovi_sameRulesAsPIX(t *testing.T) {
	// Woovi é PIX automatizado — mesma rail. Trava idêntica.
	woovi := gwFixture("woovi", "BRL")
	if gatewayEligible(woovi, "USD", "USDT", "de") {
		t.Fatal("Woovi não pode aparecer pra alemão")
	}
	if !gatewayEligible(woovi, "USD", "USDT", "br") {
		t.Fatal("Woovi deve aparecer pra brasileiro mesmo em USD")
	}
}

func TestGatewayEligible_PIX_withUSDTtypoInAccepted_stillBlocked(t *testing.T) {
	// Admin cadastra PIX com BRL + USDT na lista (erro). PIX não pode
	// aparecer pra alemão mesmo assim — provider matters more than typo.
	pix := gwFixture("manual_pix", "BRL", "USDT")
	if gatewayEligible(pix, "USD", "USDT", "de") {
		t.Fatal("PIX com USDT typo NÃO pode passar pelo filtro brOnly")
	}
}

func TestGatewayEligible_ManualCrypto_USDTuniversal(t *testing.T) {
	// Crypto provider com USDT na lista → aparece pra qualquer display.
	gw := gwFixture("manual_crypto", "USDT")
	cases := []struct {
		display, settle, country string
	}{
		{"USD", "USDT", "us"},
		{"EUR", "EUR", "de"},
		{"BRL", "BRL", "br"},
		{"GBP", "GBP", "gb"},
	}
	for _, tc := range cases {
		if !gatewayEligible(gw, tc.display, tc.settle, tc.country) {
			t.Fatalf("USDT manual_crypto deve ser universal — falhou em %+v", tc)
		}
	}
}

func TestGatewayEligible_Heleket_USDTuniversal(t *testing.T) {
	// Heleket aceita BTC + ETH + USDT — qualquer display, mostra.
	heleket := gwFixture("heleket", "USDT", "BTC", "ETH")
	if !gatewayEligible(heleket, "USD", "USDT", "us") {
		t.Fatal("Heleket com USDT deve aparecer pra USD")
	}
	if !gatewayEligible(heleket, "EUR", "EUR", "de") {
		t.Fatal("Heleket com USDT deve aparecer pra EUR")
	}
}

func TestGatewayEligible_Stripe_onlyVisibleForMatchingCurrency(t *testing.T) {
	// Stripe NÃO é crypto provider — não recebe passe USDT-universal.
	// Cliente vê Stripe SÓ se o gateway aceita a display ou settle dele.
	stripe := gwFixture("stripe", "USD", "EUR")
	if !gatewayEligible(stripe, "USD", "USDT", "us") {
		t.Fatal("Stripe USD/EUR deve aparecer pra USD")
	}
	if !gatewayEligible(stripe, "EUR", "EUR", "de") {
		t.Fatal("Stripe USD/EUR deve aparecer pra EUR")
	}
	if gatewayEligible(stripe, "BRL", "BRL", "br") {
		t.Fatal("Stripe sem BRL na lista NÃO deve aparecer pra brasileiro em BRL")
	}
	// Stripe com USDT typado na lista: NÃO recebe universal (não é crypto).
	stripeWithUSDT := gwFixture("stripe", "USD", "USDT")
	if !gatewayEligible(stripeWithUSDT, "USD", "USDT", "us") {
		t.Fatal("Stripe USD/USDT pra USD: passa pela match direta")
	}
	if gatewayEligible(stripeWithUSDT, "GBP", "GBP", "gb") {
		t.Fatal("Stripe USD/USDT NÃO deve aparecer pra GBP — USDT não é universal pra fiat provider")
	}
}

func TestGatewayEligible_Heleket_currencyMatchStillWorksIfNoUSDT(t *testing.T) {
	// Heleket configurado SEM USDT (admin desabilitou) — cai na regra de
	// match direto com display/settle.
	heleketNoUSDT := gwFixture("heleket", "BTC", "ETH")
	if !gatewayEligible(heleketNoUSDT, "BTC", "BTC", "us") {
		t.Fatal("Heleket BTC deve aparecer quando display=BTC")
	}
	if gatewayEligible(heleketNoUSDT, "USD", "USD", "us") {
		t.Fatal("Heleket sem USD na lista NÃO deve aparecer pra USD")
	}
}

func TestGatewayEligible_emptyAcceptedCurrencies_alwaysFalse(t *testing.T) {
	// Gateway mal cadastrado (vazio) — never eligible, evita crash.
	gw := gwFixture("manual_crypto")
	if gatewayEligible(gw, "USD", "USDT", "us") {
		t.Fatal("gateway sem accepted_currencies não deve passar")
	}
}

func TestGatewayEligible_caseInsensitive(t *testing.T) {
	// Robustez: admin pode digitar em minúsculo ou misturado.
	gw := domain.PaymentGateway{
		Provider:           "Manual_Crypto",
		Active:             true,
		AcceptedCurrencies: []string{"usdt", "  btc  "},
	}
	if !gatewayEligible(gw, "USD", "USDT", "us") {
		t.Fatal("filtro case-insensitive — USDT lowercase deve ser tratado")
	}
}
