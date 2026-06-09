package application

import "context"

// PaymentCustomer são os dados mínimos do cliente que o provider precisa
// (Woovi e Heleket pedem isso para emitir cobrança / antifraude).
type PaymentCustomer struct {
	Name  string
	Email string
}

// PaymentChargeInput é o que vai para o adapter do provider.
type PaymentChargeInput struct {
	OrderID     string
	Description string
	Amount      string // string formatada (ex.: "9.90", "0.00018"), decimais conforme a moeda
	Currency    string // BRL, USDT, BTC, USD, EUR
	Customer    PaymentCustomer
	Config      map[string]string // config do gateway (app_id, api_key, base_url, callback_url, ...)

	// GatewayID/Provider são populados pelos services ao montar a charge
	// (PHASE-8). Em modo legado in-memory são ignorados (cada adapter já
	// sabe quem é); em modo microservice o paymentsclient os serializa no
	// body pra o viralefy_payments resolver qual provider concreto rodar.
	GatewayID string
	Provider  string
}

// PaymentCharge é a resposta normalizada do provider.
type PaymentCharge struct {
	ExternalRef string            // id da cobrança no provider
	PaymentURL  string            // link para a tela/QR do pagamento
	Extra       map[string]string // br_code, qr_code_image, wallet, network, expires_at, ...
}

// PaymentProvider é a porta de saída para a integração com gateways de
// pagamento. Cada implementação concreta vive em infrastructure/external/payment.
type PaymentProvider interface {
	Provider() string // identificador (ex.: "woovi", "heleket", "manual_pix")
	CreateCharge(ctx context.Context, in PaymentChargeInput) (PaymentCharge, error)
}

// PaymentRegistry agrega os providers disponíveis, indexados pelo identificador.
//
// Modo legado (PHASE-7 e anterior): NewPaymentRegistry(provider1, provider2, …)
// — cada provider concreto (stripe, woovi, heleket, manual_pix…) vira uma
// entrada no map. Get(provider) retorna a entrada específica.
//
// Modo microservice (PHASE-8 Wave 3): NewRemotePaymentRegistry(client) — o
// registry encaminha TODA chamada CreateCharge pro paymentsclient. Get(any)
// retorna o mesmo wrapper remoto independente do gateway pedido — o
// microserviço resolve qual provider concreto rodar via gateway_id+provider
// no payload. CheckoutService/InvoiceService não precisam mudar; só o
// wiring em main.go.
type PaymentRegistry struct {
	providers map[string]PaymentProvider
	// fallback é consultado quando providers[provider] não tem hit. Setado
	// pelo modo microservice (NewRemotePaymentRegistry) — recebe TODO
	// CreateCharge porque o microserviço internamente faz o dispatch pelo
	// gateway. No modo legado fica nil e Get continua estritamente map-only.
	fallback PaymentProvider
}

func NewPaymentRegistry(list ...PaymentProvider) *PaymentRegistry {
	r := &PaymentRegistry{providers: map[string]PaymentProvider{}}
	for _, p := range list {
		r.providers[p.Provider()] = p
	}
	return r
}

// NewRemotePaymentRegistry cria um registry que delega TODA cobrança pro
// provider remoto (paymentsclient.Client). Não cadastra providers locais —
// any Get(x) devolve o wrapper remoto. Usado por main.go quando
// cfg.PaymentsInternalURL != "".
//
// Mantemos o tipo PaymentRegistry (mesma struct, novo construtor) pra
// preservar a assinatura de CheckoutService/InvoiceService — caso contrário
// teríamos que mexer em 2 services + handlers só pra trocar a fonte do
// charge.
func NewRemotePaymentRegistry(remote PaymentProvider) *PaymentRegistry {
	return &PaymentRegistry{
		providers: map[string]PaymentProvider{},
		fallback:  remote,
	}
}

func (r *PaymentRegistry) Get(provider string) (PaymentProvider, bool) {
	if r == nil {
		return nil, false
	}
	if p, ok := r.providers[provider]; ok {
		return p, true
	}
	if r.fallback != nil {
		return r.fallback, true
	}
	return nil, false
}
