package application

import (
	"strings"
	"testing"
)

// As listas de categorias são contrato com a UI + LGPD baseline.
// Mudanças aqui são deliberadas — não regredir silenciosamente.

func TestDataCategoriesDeleted_MinimumCoverage(t *testing.T) {
	if len(dataCategoriesDeleted) < 5 {
		t.Fatalf("dataCategoriesDeleted muito curto (%d) — coverage LGPD regrediu",
			len(dataCategoriesDeleted))
	}
	// Smoke: alguns keywords obrigatórios pra UI ficar honesta.
	mustHave := []string{"e-mail", "Tokens", "Perfis"}
	flat := strings.Join(dataCategoriesDeleted, " | ")
	for _, kw := range mustHave {
		if !strings.Contains(strings.ToLower(flat), strings.ToLower(kw)) {
			t.Errorf("dataCategoriesDeleted falta keyword %q — UI fica enganando usuário", kw)
		}
	}
}

func TestDataCategoriesRetained_MentionsFiscal(t *testing.T) {
	flat := strings.ToLower(strings.Join(dataCategoriesRetained, " | "))
	if !strings.Contains(flat, "fiscal") {
		t.Errorf("dataCategoriesRetained não menciona retenção fiscal — LGPD Art.16 esquecido")
	}
	if !strings.Contains(flat, "audit") {
		t.Errorf("dataCategoriesRetained não menciona audit log — imutabilidade esquecida")
	}
}

func TestDeletionWindowIs30Days(t *testing.T) {
	// 30 dias é piso CCPA/GDPR. Mudar isso é decisão de produto + jurídico.
	const expected = 30 * 24 * 60 * 60 // segundos
	if int64(deletionWindow.Seconds()) != expected {
		t.Errorf("deletionWindow = %v, esperado 30d (%ds). Mudar exige update da política de privacidade.",
			deletionWindow, expected)
	}
}
