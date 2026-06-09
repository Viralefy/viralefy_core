package application

import (
	"strings"
	"testing"
)

func TestReviewRequestEmail_SubjectIsEnglish(t *testing.T) {
	subject, _, _, err := BuildReviewRequestEmail(ReviewRequestEmailData{
		Name: "Jane", PlanName: "1k followers", OrderID: "abc12345",
	})
	if err != nil {
		t.Fatalf("BuildReviewRequestEmail: %v", err)
	}
	if subject != "Viralefy — how was your order?" {
		t.Errorf("subject = %q, want EN review request subject", subject)
	}
}

func TestReviewRequestEmail_HTMLDeclaresLangEN(t *testing.T) {
	_, html, _, err := BuildReviewRequestEmail(ReviewRequestEmailData{
		Name: "Jane", PlanName: "p", OrderID: "x",
	})
	if err != nil {
		t.Fatalf("BuildReviewRequestEmail: %v", err)
	}
	if !strings.Contains(html, `<html lang="en">`) {
		t.Errorf("HTML must declare lang=\"en\"")
	}
	if strings.Contains(html, "pt-BR") {
		t.Errorf("HTML must NOT leak pt-BR")
	}
}

func TestReviewRequestEmail_LinksToReviewPageWithOrderID(t *testing.T) {
	_, html, text, err := BuildReviewRequestEmail(ReviewRequestEmailData{
		SiteURL: "https://viralefy.com", Name: "Jane", PlanName: "1k followers", OrderID: "abc12345",
	})
	if err != nil {
		t.Fatalf("BuildReviewRequestEmail: %v", err)
	}
	// Link específico do order — não link genérico para /account.
	wantURL := "https://viralefy.com/orders/abc12345/review"
	if !strings.Contains(html, wantURL) {
		t.Errorf("HTML missing review link %q", wantURL)
	}
	if !strings.Contains(text, wantURL) {
		t.Errorf("text missing review link %q", wantURL)
	}
}

func TestReviewRequestEmail_GreetingUsesName(t *testing.T) {
	_, html, text, err := BuildReviewRequestEmail(ReviewRequestEmailData{
		Name: "Maria", PlanName: "p", OrderID: "x",
	})
	if err != nil {
		t.Fatalf("BuildReviewRequestEmail: %v", err)
	}
	if !strings.Contains(html, "Hi Maria") {
		t.Errorf("HTML missing greeting 'Hi Maria'")
	}
	if !strings.Contains(text, "Hi Maria") {
		t.Errorf("text missing greeting")
	}
}

func TestReviewRequestEmail_MentionsPlanNameAndGuarantee(t *testing.T) {
	_, html, _, err := BuildReviewRequestEmail(ReviewRequestEmailData{
		Name: "Jane", PlanName: "5k followers Instagram", OrderID: "x",
	})
	if err != nil {
		t.Fatalf("BuildReviewRequestEmail: %v", err)
	}
	// Plan name embutido na cópia pra dar contexto.
	if !strings.Contains(html, "5k followers Instagram") {
		t.Errorf("HTML missing plan name")
	}
	// 30-day guarantee — bate com hasMerchantReturnPolicy do JSON-LD.
	if !strings.Contains(html, "30-day guarantee") {
		t.Errorf("HTML missing 30-day guarantee mention")
	}
}

func TestReviewRequestEmail_NoPortugueseLeaks(t *testing.T) {
	_, html, text, err := BuildReviewRequestEmail(ReviewRequestEmailData{
		Name: "Jane", PlanName: "p", OrderID: "x",
	})
	if err != nil {
		t.Fatalf("BuildReviewRequestEmail: %v", err)
	}
	forbidden := []string{"Olá", "Recebemos", "Dúvidas", "Avaliação", "Como foi seu pedido"}
	for _, p := range forbidden {
		if strings.Contains(html, p) || strings.Contains(text, p) {
			t.Errorf("pt-BR leak in template: %q", p)
		}
	}
}

func TestReviewRequestEmail_DefaultsApplied(t *testing.T) {
	subject, html, _, err := BuildReviewRequestEmail(ReviewRequestEmailData{
		Name: "X", PlanName: "p", OrderID: "y",
	})
	if err != nil {
		t.Fatalf("BuildReviewRequestEmail: %v", err)
	}
	if !strings.Contains(html, "https://viralefy.com") {
		t.Errorf("expected default SiteURL")
	}
	if !strings.Contains(html, "/logo.png") {
		t.Errorf("expected default LogoURL")
	}
	if subject == "" {
		t.Errorf("subject must not be empty")
	}
}

// ---------- firstName helper ----------

func TestFirstName_SplitsOnFirstSpace(t *testing.T) {
	cases := map[string]string{
		"John Doe":           "John",
		"Maria Silva Santos": "Maria",
		"Cher":               "Cher",
		"":                   "there",
	}
	for in, want := range cases {
		if got := firstName(in); got != want {
			t.Errorf("firstName(%q) = %q, want %q", in, got, want)
		}
	}
}
