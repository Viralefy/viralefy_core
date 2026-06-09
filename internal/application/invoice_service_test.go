package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// CreateInvoiceInput.AmountCents é canônico em USD-cents desde a migração
// 011. O mínimo de 500 cents = $5.00 USD foi escolhido pra evitar lixo nos
// gateways (PIX micro-transações, taxas de Heleket etc.). Antes o comentário
// dizia "R$ 5,00" — a checagem hoje é USD.

func TestCreateInvoiceInput_RejectsZeroAmount(t *testing.T) {
	svc := NewInvoiceService(nil, nil, nil, nil, nil, nil)
	_, err := svc.Create(context.Background(), CreateInvoiceInput{
		UserID:          "user-1",
		AmountCents:     0,
		DisplayCurrency: "USD",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateInvoiceInput_RejectsNegativeAmount(t *testing.T) {
	svc := NewInvoiceService(nil, nil, nil, nil, nil, nil)
	_, err := svc.Create(context.Background(), CreateInvoiceInput{
		UserID:          "user-1",
		AmountCents:     -100,
		DisplayCurrency: "USD",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateInvoiceInput_RejectsEmptyUserID(t *testing.T) {
	svc := NewInvoiceService(nil, nil, nil, nil, nil, nil)
	_, err := svc.Create(context.Background(), CreateInvoiceInput{
		UserID:          "",
		AmountCents:     1000,
		DisplayCurrency: "USD",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestCreateInvoiceInput_RejectsBelowMinimum(t *testing.T) {
	// Mínimo = $5.00 USD = 500 USD-cents. 499 deve cair.
	// Esse limite protege os gateways de cobranças micro (PIX ~ R$ 0.01,
	// crypto fee maior que o valor, etc.). Crítico que seja USD, não BRL.
	svc := NewInvoiceService(nil, nil, nil, nil, nil, nil)
	for _, amt := range []int64{1, 100, 250, 499} {
		_, err := svc.Create(context.Background(), CreateInvoiceInput{
			UserID:          "user-1",
			AmountCents:     amt,
			DisplayCurrency: "USD",
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("AmountCents=%d should be rejected, got err=%v", amt, err)
		}
	}
}

// Garante que o critério é USD-cents, não BRL-cents — se alguém regredir o
// mínimo pra BRL R$ 5.00 (≈ $1 USD em rate atual = 100 cents), aceitar 100
// passaria. Tem que rejeitar.
func TestCreateInvoiceInput_MinimumIsUSDCentsNotBRLCents(t *testing.T) {
	svc := NewInvoiceService(nil, nil, nil, nil, nil, nil)
	// 100 USD-cents = $1.00 USD. Equivale a ~R$ 5.41. Antes do switch USD-base,
	// 500 BRL-cents = R$ 5.00 ≈ $0.92. Agora o limiar é em USD: 100 < 500, rejeita.
	_, err := svc.Create(context.Background(), CreateInvoiceInput{
		UserID:          "user-1",
		AmountCents:     100,
		DisplayCurrency: "BRL",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("expected rejection at 100 cents (= $1 USD, below $5 minimum)")
	}
}
