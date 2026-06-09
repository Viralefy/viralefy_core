package payment

import (
	"context"

	"github.com/Viralefy/viralefy_core/internal/application"
)

// ManualPIX é o provider "fallback" sem integração: o admin define uma chave
// PIX manual em gateway.config.pix_key e o cliente paga e envia o comprovante.
// Útil para começar antes de configurar Woovi/Heleket.
type ManualPIX struct{}

func NewManualPIX() *ManualPIX { return &ManualPIX{} }

func (ManualPIX) Provider() string { return "manual_pix" }

func (ManualPIX) CreateCharge(_ context.Context, in application.PaymentChargeInput) (application.PaymentCharge, error) {
	extra := map[string]string{
		"pix_key":      in.Config["pix_key"],
		"instructions": "Faça o PIX para a chave acima e envie o comprovante por e-mail.",
	}
	return application.PaymentCharge{ExternalRef: "", PaymentURL: "", Extra: extra}, nil
}
