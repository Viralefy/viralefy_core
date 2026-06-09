// Package turnstile valida tokens do Cloudflare Turnstile (anti-bot).
//
// Uso: Service.Verify(ctx, token, clientIP). Retorna nil se OK, erro com
// motivo se falhar. Quando TURNSTILE_SECRET_KEY está vazio, Service.Verify
// vira no-op — facilita HML/dev sem precisar provisionar Turnstile.
package turnstile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const verifyEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Service verifica tokens. SecretKey vazia = bypass (sempre OK).
type Service struct {
	SecretKey string
	client    *http.Client
}

func NewService(secretKey string) *Service {
	return &Service{
		SecretKey: strings.TrimSpace(secretKey),
		client:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Enabled reporta se o serviço fará verificação real (false quando
// SecretKey está vazia).
func (s *Service) Enabled() bool {
	return s.SecretKey != ""
}

// Verify valida o token. Retorna nil se OK.
// clientIP é opcional (string vazia se desconhecido) — recomendado preencher
// pra usar o anti-fraude do Cloudflare ao máximo.
func (s *Service) Verify(ctx context.Context, token, clientIP string) error {
	if !s.Enabled() {
		return nil
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("turnstile: missing token")
	}

	form := url.Values{}
	form.Set("secret", s.SecretKey)
	form.Set("response", token)
	if clientIP != "" {
		form.Set("remoteip", clientIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyEndpoint,
		bytes.NewBufferString(form.Encode()))
	if err != nil {
		return fmt.Errorf("turnstile: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("turnstile: post: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("turnstile: decode: %w", err)
	}
	if !body.Success {
		return fmt.Errorf("turnstile: verification failed (%s)",
			strings.Join(body.ErrorCodes, ","))
	}
	return nil
}
