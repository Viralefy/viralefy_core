package payment

import (
	"context"

	"github.com/Viralefy/viralefy_core/internal/application"
)

// ManualUSDT — provider "fallback" para crypto sem integração de gateway.
// Admin define uma carteira USDT fixa em gateway.config (wallet_address +
// network + opcional memo). Customer paga e admin marca como paid manualmente
// no backoffice. Útil enquanto Heleket/processor automático não aprova.
//
// Networks suportadas (livre):
//   - TRC20 (Tron) — fees baixíssimas, popular para PIX→USDT
//   - ERC20 (Ethereum) — fees altas, melhor pra valores grandes
//   - BEP20 (BSC) — fees baixas, popular
//   - Polygon — fees baixas
//   - Solana — rápido
type ManualUSDT struct{}

func NewManualUSDT() *ManualUSDT { return &ManualUSDT{} }

func (ManualUSDT) Provider() string { return "manual_usdt" }

func (ManualUSDT) CreateCharge(_ context.Context, in application.PaymentChargeInput) (application.PaymentCharge, error) {
	extra := map[string]string{
		"wallet_address": in.Config["wallet_address"],
		"network":        in.Config["network"],
		"amount":         in.Amount,
		"currency":       in.Currency,
		"instructions":   "Send the exact amount to the wallet above. Include the memo if present. Your order will be activated once the deposit is confirmed.",
	}
	if memo := in.Config["memo"]; memo != "" {
		extra["memo"] = memo
	}
	return application.PaymentCharge{ExternalRef: "", PaymentURL: "", Extra: extra}, nil
}
