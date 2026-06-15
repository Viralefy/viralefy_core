package payment

import (
	"net"
	"net/http"
	"time"
)

// DefaultHTTPClient retorna um *http.Client com Transport customizado pros
// adapters de payment provider. http.DefaultTransport é razoável pra browser,
// mas pra chamadas server-to-server contra gateways (Stripe/Woovi/Heleket)
// queremos timeouts agressivos em cada camada (dial / TLS / response headers),
// não só um Timeout total. Isso evita:
//
//   - Conexões TCP penduradas (DNS lento ou IP morto) consumindo a janela
//     de timeout total do request.
//   - TLS handshake estagnado segurar o worker.
//   - Provider aceitar a conexão mas nunca enviar header (slowloris) —
//     ResponseHeaderTimeout corta isso bem antes do Timeout total.
//
// Pool limitado (MaxIdleConnsPerHost=10) evita explosão de FDs sob burst.
// timeout é o teto absoluto end-to-end (read+write inclusive).
func DefaultHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   3 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}
