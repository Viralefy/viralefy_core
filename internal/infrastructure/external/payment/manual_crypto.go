package payment

import (
	"context"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/application"
)

// ManualCrypto — provider genérico pra crypto sem integração on-chain.
// Cada gateway-row representa UMA (network × asset): admin cadastra
// "USDT TRC20", "USDT BSC", "USDT POL", "BTC", "LTC" etc. como gateways
// independentes, cada um com seu wallet_address fixo. O checkout filtra
// por accepted_currencies e o cliente escolhe a rede no step de seleção.
//
// Config esperado por gateway:
//   network          — TRC20, BEP20, ERC20, Polygon, Bitcoin, Litecoin, Solana...
//   wallet_address   — endereço fixo de recebimento (admin owns the key)
//   memo             — opcional (Solana, alguns custodial)
//   network_label    — opcional, override visual ("USDT (Tron)")
//   network_warning  — opcional, override do aviso default
//
// AVISO DE REDE é crítico: USDT TRC20 ≠ USDT BSC ≠ USDT ERC20. Depósito
// em rede errada = fundos perdidos. UI deve destacar.
type ManualCrypto struct{}

func NewManualCrypto() *ManualCrypto { return &ManualCrypto{} }

func (ManualCrypto) Provider() string { return "manual_crypto" }

func (ManualCrypto) CreateCharge(_ context.Context, in application.PaymentChargeInput) (application.PaymentCharge, error) {
	network := strings.TrimSpace(in.Config["network"])
	warning := strings.TrimSpace(in.Config["network_warning"])
	if warning == "" && network != "" {
		warning = "Send ONLY on " + network + ". Deposits on any other network will be lost forever."
	}
	extra := map[string]string{
		"method_kind":    "crypto_manual",
		"wallet_address": in.Config["wallet_address"],
		"network":        network,
		"network_label":  defaultStr(in.Config["network_label"], network),
		"amount":         in.Amount,
		"currency":       in.Currency,
		"instructions":   "Send the exact amount to the wallet above. Once the deposit is on-chain, upload your transaction hash or screenshot below — we activate the order after confirmation.",
		"network_warning": warning,
	}
	if memo := strings.TrimSpace(in.Config["memo"]); memo != "" {
		extra["memo"] = memo
		extra["memo_warning"] = "The memo/tag is REQUIRED — without it we cannot identify your deposit."
	}
	return application.PaymentCharge{ExternalRef: "", PaymentURL: "", Extra: extra}, nil
}

func defaultStr(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
