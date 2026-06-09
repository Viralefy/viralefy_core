package application

import (
	"strings"
	"testing"
)

// Garante que os templates de e-mail estão em inglês — política do projeto
// é EN-default (cliente é global; storefront não tem mais localização por
// usuário no e-mail). Localização por user.locale fica como follow-up.

func TestCheckoutEmail_SubjectIsEnglish(t *testing.T) {
	tests := []struct {
		name           string
		accountCreated bool
		want           string
	}{
		{"new account", true, "Viralefy — your account and order"},
		{"existing user", false, "Viralefy — order received"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := CheckoutEmailData{
				Name:               "Jane",
				Email:              "jane@example.com",
				PlanName:           "1,000 followers",
				DisplayCurrency:    "USD",
				DisplaySymbol:      "$",
				DisplayAmount:      "9.90",
				SettlementCurrency: "USDT",
				SettlementAmount:   "9.90",
				AccountCreated:     tc.accountCreated,
				Password:           "p4ss",
				PaymentURL:         "https://pay.example/123",
			}
			subject, _, _, err := BuildCheckoutEmail(d)
			if err != nil {
				t.Fatalf("BuildCheckoutEmail: %v", err)
			}
			if subject != tc.want {
				t.Errorf("subject = %q, want %q", subject, tc.want)
			}
		})
	}
}

func TestCheckoutEmail_HTMLLangIsEnglish(t *testing.T) {
	_, html, _, err := BuildCheckoutEmail(CheckoutEmailData{
		Name: "Jane", Email: "j@x", PlanName: "p",
		DisplayCurrency: "USD", DisplaySymbol: "$", DisplayAmount: "1",
		SettlementCurrency: "USDT", SettlementAmount: "1",
	})
	if err != nil {
		t.Fatalf("BuildCheckoutEmail: %v", err)
	}
	if !strings.Contains(html, `<html lang="en">`) {
		t.Errorf("HTML must declare lang=\"en\" — got snippet: %s", html[:200])
	}
	if strings.Contains(html, `lang="pt-BR"`) {
		t.Errorf("HTML must NOT declare lang=\"pt-BR\"")
	}
}

func TestCheckoutEmail_NoPortugueseLeaks(t *testing.T) {
	// Strings pt-BR que ESTAVAM nos templates antes do EN sweep. Qualquer
	// reaparecimento é regressão.
	forbidden := []string{
		"Olá",
		"Dúvidas",
		"Recebemos seu pedido",
		"Conta criada",
		"Como pagar",
		"Cobrança",
		"Recarga",
		"Senha:",      // PT label (em EN é "Password:")
		"E-mail:",     // PT label com hífen (EN usa "Email:")
		"copia-e-cola",
		"Abra um ticket de suporte",
		"Abrir página de pagamento",
		"Ir para o pagamento",
	}
	d := CheckoutEmailData{
		Name:               "Jane",
		Email:              "jane@example.com",
		PlanName:           "1,000 followers",
		DisplayCurrency:    "USD",
		DisplaySymbol:      "$",
		DisplayAmount:      "9.90",
		SettlementCurrency: "USDT",
		SettlementAmount:   "9.90",
		AccountCreated:     true,
		Password:           "p4ss",
		BrCode:             "PIX-CODE",
		QrImage:            "data:image/png;base64,XXX",
		CryptoAddress:      "0xWALLET",
		CryptoNetwork:      "ERC20",
		PixKey:             "key@pix",
		PaymentURL:         "https://pay.example/123",
	}
	_, html, text, err := BuildCheckoutEmail(d)
	if err != nil {
		t.Fatalf("BuildCheckoutEmail: %v", err)
	}
	for _, phrase := range forbidden {
		if strings.Contains(html, phrase) {
			t.Errorf("HTML leaks pt-BR string %q", phrase)
		}
		if strings.Contains(text, phrase) {
			t.Errorf("text leaks pt-BR string %q", phrase)
		}
	}
}

func TestCheckoutEmail_EnglishMarkersPresent(t *testing.T) {
	// Confirma POSITIVAMENTE que os blocos EN estão renderizando.
	d := CheckoutEmailData{
		Name: "Jane", Email: "j@x", PlanName: "p",
		DisplayCurrency: "USD", DisplaySymbol: "$", DisplayAmount: "1.00",
		SettlementCurrency: "USDT", SettlementAmount: "1.00",
		AccountCreated: true, Password: "p4ss", PaymentURL: "https://pay/x",
	}
	_, html, text, err := BuildCheckoutEmail(d)
	if err != nil {
		t.Fatalf("BuildCheckoutEmail: %v", err)
	}
	wantHTML := []string{
		"Hi Jane",
		"We received your order",
		"How to pay",
		"Account created",
		"Email:",
		"Password:",
		"Questions?",
	}
	for _, s := range wantHTML {
		if !strings.Contains(html, s) {
			t.Errorf("HTML missing EN marker %q", s)
		}
	}
	wantText := []string{
		"Hi Jane!",
		"How to pay:",
		"Questions? Open a ticket",
	}
	for _, s := range wantText {
		if !strings.Contains(text, s) {
			t.Errorf("text missing EN marker %q", s)
		}
	}
}

func TestTicketReplyEmail_SubjectAndContentEnglish(t *testing.T) {
	d := TicketReplyEmailData{
		Name:     "Jane",
		Subject:  "Order not delivered",
		Body:     "Could you check?",
		TicketID: "abc12345",
	}
	subject, html, text, err := BuildTicketReplyEmail(d)
	if err != nil {
		t.Fatalf("BuildTicketReplyEmail: %v", err)
	}
	if subject != "Viralefy — support reply" {
		t.Errorf("subject = %q, want EN reply subject", subject)
	}
	if !strings.Contains(html, `<html lang="en">`) {
		t.Errorf("HTML lang must be \"en\"")
	}
	for _, s := range []string{"Hi Jane", "Support replied to your ticket", "Open conversation"} {
		if !strings.Contains(html, s) {
			t.Errorf("HTML missing EN marker %q", s)
		}
	}
	for _, s := range []string{"Hi Jane!", "Support replied to your ticket", "Open the conversation"} {
		if !strings.Contains(text, s) {
			t.Errorf("text missing EN marker %q", s)
		}
	}
	// Regressão: não pode ter pt-BR.
	for _, s := range []string{"Olá", "suporte respondeu", "Abrir conversa"} {
		if strings.Contains(html, s) {
			t.Errorf("HTML leaks pt-BR string %q", s)
		}
	}
}

func TestCheckoutEmail_SettlementSuffixOnlyWhenDifferent(t *testing.T) {
	// Quando display=settlement (ex.: USD/USDT no mesmo valor) NÃO deve
	// mostrar o sufixo "Charged in X" — só polui.
	d := CheckoutEmailData{
		Name: "Jane", Email: "j@x", PlanName: "p",
		DisplayCurrency: "USDT", DisplaySymbol: "$", DisplayAmount: "1.00",
		SettlementCurrency: "USDT", SettlementAmount: "1.00",
	}
	_, html, _, err := BuildCheckoutEmail(d)
	if err != nil {
		t.Fatalf("BuildCheckoutEmail: %v", err)
	}
	if strings.Contains(html, "Charged in") {
		t.Error("Charged-in suffix should NOT appear when display == settlement")
	}

	// Quando display != settlement, deve aparecer.
	d.SettlementCurrency = "BRL"
	d.SettlementAmount = "5.41"
	_, html2, _, _ := BuildCheckoutEmail(d)
	if !strings.Contains(html2, "Charged in") {
		t.Error("Charged-in suffix should appear when display != settlement")
	}
	if !strings.Contains(html2, "5.41 BRL") {
		t.Errorf("expected settlement amount + currency in HTML")
	}
}

func TestCheckoutEmail_DefaultsAreSet(t *testing.T) {
	// SiteURL e LogoURL devem ter defaults quando omitidos.
	subject, html, _, err := BuildCheckoutEmail(CheckoutEmailData{
		Name: "X", Email: "x@x", PlanName: "p",
		DisplayCurrency: "USDT", DisplaySymbol: "$", DisplayAmount: "1",
		SettlementCurrency: "USDT", SettlementAmount: "1",
	})
	if err != nil {
		t.Fatalf("BuildCheckoutEmail: %v", err)
	}
	if !strings.Contains(html, "https://viralefy.com") {
		t.Errorf("expected default SiteURL injected into HTML")
	}
	if !strings.Contains(html, "/logo.png") {
		t.Errorf("expected default LogoURL")
	}
	if subject == "" {
		t.Errorf("subject must not be empty")
	}
}
