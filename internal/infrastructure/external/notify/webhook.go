// Package notify dispara notificações administrativas via webhook genérico
// (Slack/Discord/Telegram compatíveis).
//
// O payload segue o formato Slack-incoming-webhook ({"text": "..."}), que
// Discord também aceita via endpoint legacy. Pra Telegram, o caller é
// responsável por usar um proxy intermediário (n8n, Zapier) ou trocar o
// Send pra format específico do Bot API.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type WebhookClient struct {
	URL    string
	client *http.Client
}

func NewWebhookClient(url string) *WebhookClient {
	return &WebhookClient{
		URL:    strings.TrimSpace(url),
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Enabled diz se há URL configurada. Quando vazia, Send vira no-op (handy
// pra HML).
func (c *WebhookClient) Enabled() bool {
	return c.URL != ""
}

// Send dispara o payload `{"text": <text>}` para a URL configurada. Erros
// são retornados pro caller decidir (logar e seguir é o caminho normal —
// notificação falhar não deve quebrar pagamento).
func (c *WebhookClient) Send(ctx context.Context, text string) error {
	if !c.Enabled() {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL,
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: webhook responded %d", resp.StatusCode)
	}
	return nil
}
