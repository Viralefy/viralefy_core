package application

import (
	"context"
	"testing"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// fakeCurrencyRepo é um repo in-memory pra testes. Implementa o
// domain.CurrencyRepository sem tocar Postgres.
type fakeCurrencyRepo struct {
	byCode map[string]domain.Currency
}

func newFakeCurrencyRepo() *fakeCurrencyRepo {
	// Espelha exatamente o seed (seedCurrencies em postgres/seed.go) — se o
	// seed mudar, o teste tem que mudar junto. Mantemos USD = base com
	// rate=1 e USDT idem (1:1).
	return &fakeCurrencyRepo{byCode: map[string]domain.Currency{
		"USDT": {Code: "USDT", Name: "Tether", Symbol: "$", Rate: 1.0, Decimals: 2, Kind: "crypto", DisplayEnabled: true, SettlementCode: "USDT"},
		"USD":  {Code: "USD", Name: "Dolar", Symbol: "$", Rate: 1.0, Decimals: 2, Kind: "fiat", DisplayEnabled: true, SettlementCode: "USDT"},
		"EUR":  {Code: "EUR", Name: "Euro", Symbol: "€", Rate: 0.92, Decimals: 2, Kind: "fiat", DisplayEnabled: true, SettlementCode: "EUR"},
		"BRL":  {Code: "BRL", Name: "Real", Symbol: "R$", Rate: 5.41, Decimals: 2, Kind: "fiat", DisplayEnabled: true, SettlementCode: "BRL"},
		"BTC":  {Code: "BTC", Name: "Bitcoin", Symbol: "₿", Rate: 0.0000103, Decimals: 8, Kind: "crypto", DisplayEnabled: true, SettlementCode: "BTC"},
		"XYZ":  {Code: "XYZ", Name: "Disabled", Symbol: "?", Rate: 1.0, Decimals: 2, Kind: "fiat", DisplayEnabled: false, SettlementCode: "USD"},
	}}
}

func (f *fakeCurrencyRepo) ListAll(ctx context.Context) ([]domain.Currency, error) {
	out := make([]domain.Currency, 0, len(f.byCode))
	for _, c := range f.byCode {
		out = append(out, c)
	}
	return out, nil
}
func (f *fakeCurrencyRepo) ListDisplayable(ctx context.Context) ([]domain.Currency, error) {
	out := []domain.Currency{}
	for _, c := range f.byCode {
		if c.DisplayEnabled {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeCurrencyRepo) GetByCode(ctx context.Context, code string) (*domain.Currency, error) {
	c, ok := f.byCode[code]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}
func (f *fakeCurrencyRepo) UpdateRate(ctx context.Context, code string, rate float64, enabled bool, settlement string) error {
	c, ok := f.byCode[code]
	if !ok {
		return domain.ErrNotFound
	}
	c.Rate = rate
	c.DisplayEnabled = enabled
	c.SettlementCode = settlement
	f.byCode[code] = c
	return nil
}

func newSvc() *CurrencyService {
	// Plan repo é nil porque esses testes não exercem cascade — Update
	// pula RecomputePricesForCurrency quando plans==nil (ver currency_service.go).
	return NewCurrencyService(newFakeCurrencyRepo(), nil)
}

// ---------- QuoteForPlan: USD-cents canônico ----------

func TestQuoteForPlan_UsesManualPriceWhenPresent(t *testing.T) {
	svc := newSvc()
	prices := map[string]string{"USD": "9.90", "BRL": "55.00", "EUR": "9.10"}
	q, err := svc.QuoteForPlan(context.Background(), prices, 990 /* USD cents */, "BRL")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.DisplayCurrency != "BRL" {
		t.Errorf("DisplayCurrency = %q, want BRL", q.DisplayCurrency)
	}
	if q.DisplayAmount != "55.00" {
		t.Errorf("DisplayAmount = %q, want manual 55.00 (NOT 990/100*5.41=53.56)", q.DisplayAmount)
	}
	if q.DisplaySymbol != "R$" {
		t.Errorf("DisplaySymbol = %q, want R$", q.DisplaySymbol)
	}
}

func TestQuoteForPlan_DerivesFromUSDCentsWhenNoManualPrice(t *testing.T) {
	svc := newSvc()
	// prices não tem EUR — deve derivar de 990 USD cents * 0.92 = 9.10 (decimals=2 → "9.11").
	// 990/100 = 9.90 * 0.92 = 9.108 → format with 2 decimals = "9.11"
	prices := map[string]string{"USD": "9.90", "BRL": "55.00"}
	q, err := svc.QuoteForPlan(context.Background(), prices, 990, "EUR")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.DisplayCurrency != "EUR" {
		t.Errorf("DisplayCurrency = %q, want EUR", q.DisplayCurrency)
	}
	if q.DisplayAmount != "9.11" {
		t.Errorf("DisplayAmount = %q, want 9.11 (derived from 990c*0.92)", q.DisplayAmount)
	}
}

func TestQuoteForPlan_USDTMirrors1To1WithUSDcents(t *testing.T) {
	// USDT é a moeda canônica do storefront — sempre 1:1 com USD.
	svc := newSvc()
	q, err := svc.QuoteForPlan(context.Background(), nil, 2500, "USDT")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.DisplayCurrency != "USDT" {
		t.Errorf("DisplayCurrency = %q, want USDT", q.DisplayCurrency)
	}
	if q.DisplayAmount != "25.00" {
		t.Errorf("DisplayAmount = %q, want 25.00 (1:1 USDT/USD)", q.DisplayAmount)
	}
	if q.DisplaySymbol != "$" {
		t.Errorf("DisplaySymbol = %q, want $", q.DisplaySymbol)
	}
}

func TestQuoteForPlan_BTCHonors8Decimals(t *testing.T) {
	svc := newSvc()
	// 2500 USD-cents = $25 → 25 * 0.0000103 = 0.000258 (8 decimals → "0.00025750")
	q, err := svc.QuoteForPlan(context.Background(), nil, 2500, "BTC")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.DisplayAmount != "0.00025750" {
		t.Errorf("DisplayAmount = %q, want 0.00025750 (8 BTC decimals)", q.DisplayAmount)
	}
}

func TestQuoteForPlan_EmptyDisplayCodeFallsBackToUSD(t *testing.T) {
	svc := newSvc()
	q, err := svc.QuoteForPlan(context.Background(), nil, 1000, "")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.DisplayCurrency != "USD" {
		t.Errorf("DisplayCurrency = %q, want USD fallback", q.DisplayCurrency)
	}
}

func TestQuoteForPlan_InvalidCurrencyFallsBackToUSD(t *testing.T) {
	svc := newSvc()
	q, err := svc.QuoteForPlan(context.Background(), nil, 1000, "ZZZ")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.DisplayCurrency != "USD" {
		t.Errorf("DisplayCurrency = %q, want USD fallback for unknown code", q.DisplayCurrency)
	}
}

func TestQuoteForPlan_DisplayDisabledFallsBackToUSD(t *testing.T) {
	svc := newSvc()
	// XYZ existe mas tem display_enabled=false — deve cair em USD.
	q, err := svc.QuoteForPlan(context.Background(), nil, 1000, "XYZ")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.DisplayCurrency != "USD" {
		t.Errorf("DisplayCurrency = %q, want USD fallback for disabled display", q.DisplayCurrency)
	}
}

func TestQuoteForPlan_SettlementResolvedFromDisplayCurrency(t *testing.T) {
	// USD exibe mas liquidação é em USDT (evita bandeira).
	svc := newSvc()
	q, err := svc.QuoteForPlan(context.Background(), nil, 1000, "USD")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.DisplayCurrency != "USD" || q.SettlementCurrency != "USDT" {
		t.Errorf("USD display should settle as USDT, got display=%q settle=%q",
			q.DisplayCurrency, q.SettlementCurrency)
	}
}

func TestQuoteForPlan_BRLSettlesAsItself(t *testing.T) {
	// BRL exibe e liquida em BRL (PIX local).
	svc := newSvc()
	q, err := svc.QuoteForPlan(context.Background(), nil, 1000, "BRL")
	if err != nil {
		t.Fatalf("QuoteForPlan: %v", err)
	}
	if q.SettlementCurrency != "BRL" {
		t.Errorf("BRL should settle as BRL, got %q", q.SettlementCurrency)
	}
	if q.SettlementSymbol != "R$" {
		t.Errorf("BRL settlement symbol = %q, want R$", q.SettlementSymbol)
	}
}

// ---------- Update validation ----------

func TestUpdate_RejectsZeroOrNegativeRate(t *testing.T) {
	svc := newSvc()
	for _, rate := range []float64{0, -1, -0.001} {
		_, err := svc.Update(context.Background(), UpdateCurrencyInput{
			Code: "EUR", Rate: rate, DisplayEnabled: true, SettlementCode: "EUR",
		})
		if err == nil {
			t.Errorf("Update should reject rate=%v", rate)
		}
	}
}

func TestUpdate_RejectsEmptySettlementCode(t *testing.T) {
	svc := newSvc()
	_, err := svc.Update(context.Background(), UpdateCurrencyInput{
		Code: "EUR", Rate: 0.92, DisplayEnabled: true, SettlementCode: "",
	})
	if err == nil {
		t.Errorf("Update should reject empty settlement_code")
	}
}

func TestUpdate_RejectsUnknownSettlementCode(t *testing.T) {
	svc := newSvc()
	_, err := svc.Update(context.Background(), UpdateCurrencyInput{
		Code: "EUR", Rate: 0.92, DisplayEnabled: true, SettlementCode: "ZZZ",
	})
	if err == nil {
		t.Errorf("Update should reject unknown settlement_code")
	}
}

func TestUpdate_AppliesChangesAndReturnsUpdated(t *testing.T) {
	svc := newSvc()
	updated, err := svc.Update(context.Background(), UpdateCurrencyInput{
		Code: "EUR", Rate: 1.10, DisplayEnabled: false, SettlementCode: "USD",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Rate != 1.10 || updated.DisplayEnabled != false || updated.SettlementCode != "USD" {
		t.Errorf("update did not persist: %+v", updated)
	}
}
