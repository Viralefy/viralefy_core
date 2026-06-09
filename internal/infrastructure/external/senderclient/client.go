// Package senderclient é o wrapper HTTP que o monolito viralefy_core usa
// pra falar com o microserviço viralefy_sender (loopback-only, porta 8082).
//
// Implementa application.EmailSender via POST /internal/v1/send com
// channel="email". Também expõe SendTelegram pra integração futura
// (channel="telegram") — não está na interface application porque o domínio
// do monolito ainda não dispara telegram diretamente; apenas o
// PaymentReceiver.MarkOrderPaid vai chamar (Wave 3).
//
// Princípios (PHASE-8 §3):
//   - X-Internal-Token em todo request.
//   - Timeout 30s — sender é fire-and-forget mas o sender_outbox grava
//     sincronamente, render do template + INSERT no Postgres pode pegar
//     latência em pico.
//   - Path /internal/v1/... versionado.
package senderclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Viralefy/viralefy_core/internal/application"
)

const (
	defaultTimeout = 30 * time.Second
	headerToken    = "X-Internal-Token"

	channelEmail    = "email"
	channelTelegram = "telegram"
)

// Client é o cliente HTTP do viralefy_sender. Stateless.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New cria o cliente. baseURL ex: "http://127.0.0.1:8082"; token é
// INTERNAL_SHARED_SECRET (mesmo do paymentsclient — segredo único).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

// sendRequest é o corpo de POST /internal/v1/send (PHASE-8 §2).
//
// Para channel="email" o monolito hoje envia HTMLBody/TextBody pré-rendered
// (legado pre-microservice). O sender_microservice de Wave 2 vai preferir
// template+vars, mas mantemos os campos raw pra compat durante migração:
// se Template está vazio, o sender renderiza o "passthrough" usando o
// HTML/Text do payload.
type sendRequest struct {
	Channel  string            `json:"channel"`
	Template string            `json:"template,omitempty"`
	To       sendTo            `json:"to"`
	Subject  string            `json:"subject,omitempty"`
	HTMLBody string            `json:"html_body,omitempty"`
	TextBody string            `json:"text_body,omitempty"`
	Vars     map[string]string `json:"vars,omitempty"`
	Priority string            `json:"priority,omitempty"`
}

type sendTo struct {
	Email          string `json:"email,omitempty"`
	TelegramHandle string `json:"telegram_handle,omitempty"`
	WebhookURL     string `json:"webhook_url,omitempty"`
}

type sendResponse struct {
	Status    string `json:"status"`
	AttemptID string `json:"attempt_id"`
}

// Send satisfaz application.EmailSender. Empacota EmailMessage como
// channel="email" passthrough. Não retorna o attempt_id (interface
// application não tem campo pra isso); status != "failed" basta.
func (c *Client) Send(ctx context.Context, msg application.EmailMessage) error {
	body := sendRequest{
		Channel:  channelEmail,
		To:       sendTo{Email: msg.To},
		Subject:  msg.Subject,
		HTMLBody: msg.HTMLBody,
		TextBody: msg.TextBody,
		Priority: "normal",
	}
	var out sendResponse
	if err := c.doJSON(ctx, http.MethodPost, "/internal/v1/send", body, &out); err != nil {
		return err
	}
	if out.Status == "failed" {
		return fmt.Errorf("senderclient: send failed (attempt_id=%s)", out.AttemptID)
	}
	return nil
}

// SendTemplate satisfaz application.TemplatedEmailer. Dispara um email
// renderizado pelo sender usando template+vars (em vez de HTML/Text
// pré-rendered). PHASE-8 Wave 3 introduz a notificação checkout_paid pelo
// PaymentReceiver — separada do "order created" do checkout pra que cliente
// receba dois emails distintos no fluxo (criação → confirmação).
func (c *Client) SendTemplate(ctx context.Context, to, template string, vars map[string]string) error {
	body := sendRequest{
		Channel:  channelEmail,
		Template: template,
		To:       sendTo{Email: to},
		Vars:     vars,
		Priority: "normal",
	}
	var out sendResponse
	if err := c.doJSON(ctx, http.MethodPost, "/internal/v1/send", body, &out); err != nil {
		return err
	}
	if out.Status == "failed" {
		return fmt.Errorf("senderclient: template send failed (attempt_id=%s)", out.AttemptID)
	}
	return nil
}

// TelegramMessage é o payload pra channel="telegram". Handle pode ser
// "@user" ou um chat_id numérico — o sender resolve via tabela
// telegram_chats (PHASE-8 §2). Vars alimenta o template Markdown V2.
type TelegramMessage struct {
	Handle   string            // "@username" ou chat_id como string
	Template string            // nome do template registrado no sender
	Vars     map[string]string // substituições do template
}

// SendTelegram (positional) satisfaz application.TelegramNotifier — o
// PaymentReceiver chama (handle, template, vars) sem importar este pacote.
// Assinatura plana evita type alias e o import-cycle senderclient ↔ application.
func (c *Client) SendTelegram(ctx context.Context, handle, template string, vars map[string]string) error {
	return c.sendTelegramRaw(ctx, TelegramMessage{Handle: handle, Template: template, Vars: vars})
}

// SendTelegramMsg é a versão struct-based — mantida pra call sites que já
// montam TelegramMessage explicitamente (futuro: backoffice manda blast
// admin). Internamente compartilha sendTelegramRaw.
func (c *Client) SendTelegramMsg(ctx context.Context, msg TelegramMessage) error {
	return c.sendTelegramRaw(ctx, msg)
}

func (c *Client) sendTelegramRaw(ctx context.Context, msg TelegramMessage) error {
	body := sendRequest{
		Channel:  channelTelegram,
		Template: msg.Template,
		To:       sendTo{TelegramHandle: msg.Handle},
		Vars:     msg.Vars,
		Priority: "normal",
	}
	var out sendResponse
	if err := c.doJSON(ctx, http.MethodPost, "/internal/v1/send", body, &out); err != nil {
		return err
	}
	if out.Status == "failed" {
		return fmt.Errorf("senderclient: telegram send failed (attempt_id=%s)", out.AttemptID)
	}
	return nil
}

// doJSON centraliza serialização, X-Internal-Token, e tratamento de erro
// HTTP. Idêntico em estrutura ao do paymentsclient — mantemos duplicação
// proposital: cada wrapper isolado pra evitar criar um pacote "internalhttp"
// prematuro (PHASE-8 evolui rápido, dependências cruzadas atrasam migração).
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("senderclient: marshal: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("senderclient: new request: %w", err)
	}
	req.Header.Set(headerToken, c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("senderclient: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("senderclient: %s %s: HTTP %d: %s",
			method, path, resp.StatusCode, truncate(string(respBody), 300))
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("senderclient: decode %s: %w", path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
