// Package metrics tira snapshots públicos de perfis e publicações no
// Instagram e TikTok. Best-effort — Meta/ByteDance fightam scrapers, então
// a função retorna (metrics, source, err) onde:
//   - source = "og_scrape" quando o parse OG funcionou
//   - source = "html_fallback" quando precisamos olhar o HTML interno
//   - err não-nil quando nem og nem html resolveram (operador preenche manual)
//
// O objetivo é ter UMA segunda fonte de verdade pra confirmar entrega do
// gateway (delivery - baseline ≈ followers_qty do plano). Não substitui o
// gateway — só nos protege quando o gateway lie.
package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Snapshot struct {
	Followers int64    `json:"followers,omitempty"`
	Following int64    `json:"following,omitempty"`
	Posts     int64    `json:"posts,omitempty"`
	Likes     int64    `json:"likes,omitempty"`
	Comments  int64    `json:"comments,omitempty"`
	Views     int64    `json:"views,omitempty"`
	Shares    int64    `json:"shares,omitempty"`
	Username  string   `json:"username,omitempty"`
	Title     string   `json:"title,omitempty"`
	URL       string   `json:"url,omitempty"`
	FetchedAt string   `json:"fetched_at,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

type Service struct {
	client *http.Client
}

func NewService() *Service {
	return &Service{
		// Pequeno timeout: scrape não pode segurar request admin. 10s é mais
		// que suficiente pra a maioria dos casos, e falhas viram fallback.
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// CaptureProfile pega métricas de um @ no Instagram ou TikTok.
// Quando falha, retorna Snapshot{} + erro — caller pode marcar source
// como "manual_pending" e disparar alerta pro operador.
func (s *Service) CaptureProfile(ctx context.Context, platform, handle string) (Snapshot, string, error) {
	handle = strings.TrimPrefix(strings.TrimSpace(handle), "@")
	if handle == "" {
		return Snapshot{}, "", fmt.Errorf("metrics: empty handle")
	}
	var pageURL string
	switch strings.ToLower(platform) {
	case "instagram":
		pageURL = "https://www.instagram.com/" + url.PathEscape(handle) + "/"
	case "tiktok":
		pageURL = "https://www.tiktok.com/@" + url.PathEscape(handle)
	default:
		return Snapshot{}, "", fmt.Errorf("metrics: unsupported platform %q", platform)
	}
	body, err := s.fetch(ctx, pageURL)
	if err != nil {
		return Snapshot{}, "", err
	}
	snap := Snapshot{Username: handle, URL: pageURL, FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	source := parseProfilePage(strings.ToLower(platform), body, &snap)
	if snap.Followers == 0 && snap.Likes == 0 && snap.Views == 0 {
		return snap, source, fmt.Errorf("metrics: no signal extracted")
	}
	return snap, source, nil
}

// CapturePublication pega métricas de uma publicação (post Instagram /
// vídeo TikTok) a partir da URL pública.
func (s *Service) CapturePublication(ctx context.Context, postURL string) (Snapshot, string, error) {
	postURL = strings.TrimSpace(postURL)
	if postURL == "" {
		return Snapshot{}, "", fmt.Errorf("metrics: empty url")
	}
	body, err := s.fetch(ctx, postURL)
	if err != nil {
		return Snapshot{}, "", err
	}
	snap := Snapshot{URL: postURL, FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	platform := ""
	switch {
	case strings.Contains(postURL, "instagram.com"):
		platform = "instagram"
	case strings.Contains(postURL, "tiktok.com"):
		platform = "tiktok"
	}
	source := parsePublicationPage(platform, body, &snap)
	if snap.Likes == 0 && snap.Views == 0 && snap.Comments == 0 {
		return snap, source, fmt.Errorf("metrics: no signal extracted")
	}
	return snap, source, nil
}

func (s *Service) fetch(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	// User-Agent realista — sem isso Meta devolve HTML quase vazio.
	// Mesmo com UA real, eles retornam server-rendered limitado pra
	// requests sem cookie/login. OG tags ainda vêm.
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 "+
			"(KHTML, like Gecko) Version/17.5 Safari/605.1.15")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("metrics: status %d", resp.StatusCode)
	}
	// Limita leitura a 2 MB — páginas raramente passam disso e queremos
	// proteger contra response gigante.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---- Parsers ---- //

// reOGDescription pega <meta property="og:description" content="..."> e
// extrai os números dali. Instagram retorna algo como "1,234 Followers,
// 567 Following, 89 Posts - See ...". TikTok retorna "@nick · X Followers,
// Y Likes, Z Following".
var (
	reOGDesc      = regexp.MustCompile(`<meta\s+property="og:description"\s+content="([^"]+)"`)
	reOGTitle     = regexp.MustCompile(`<meta\s+property="og:title"\s+content="([^"]+)"`)
	reFollowersIG = regexp.MustCompile(`([\d.,]+)\s*Followers`)
	reFollowingIG = regexp.MustCompile(`([\d.,]+)\s*Following`)
	rePostsIG     = regexp.MustCompile(`([\d.,]+)\s*Posts`)
	reLikesEN     = regexp.MustCompile(`([\d.,]+)\s*Likes`)
	reCommentsEN  = regexp.MustCompile(`([\d.,]+)\s*[Cc]omments?`)
	reViewsEN     = regexp.MustCompile(`([\d.,]+)\s*[Vv]iews?`)
)

func parseProfilePage(platform, body string, snap *Snapshot) string {
	desc := findFirst(reOGDesc, body)
	title := findFirst(reOGTitle, body)
	if title != "" {
		snap.Title = title
	}
	if desc == "" {
		snap.Errors = append(snap.Errors, "missing og:description")
		return "og_scrape_empty"
	}
	if v := parseCount(reFollowersIG, desc); v > 0 {
		snap.Followers = v
	}
	if v := parseCount(reFollowingIG, desc); v > 0 {
		snap.Following = v
	}
	if v := parseCount(rePostsIG, desc); v > 0 {
		snap.Posts = v
	}
	if platform == "tiktok" {
		if v := parseCount(reLikesEN, desc); v > 0 {
			snap.Likes = v
		}
	}
	return "og_scrape"
}

func parsePublicationPage(_ string, body string, snap *Snapshot) string {
	desc := findFirst(reOGDesc, body)
	title := findFirst(reOGTitle, body)
	if title != "" {
		snap.Title = title
	}
	if desc == "" {
		snap.Errors = append(snap.Errors, "missing og:description")
		return "og_scrape_empty"
	}
	if v := parseCount(reLikesEN, desc); v > 0 {
		snap.Likes = v
	}
	if v := parseCount(reCommentsEN, desc); v > 0 {
		snap.Comments = v
	}
	if v := parseCount(reViewsEN, desc); v > 0 {
		snap.Views = v
	}
	return "og_scrape"
}

func findFirst(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// parseCount transforma "1.2K", "1,234", "1.234.567" em int64.
// Instagram usa abreviação K/M/B; TikTok também. Português abrevia "mil".
func parseCount(re *regexp.Regexp, s string) int64 {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	raw := strings.TrimSpace(m[1])
	mult := int64(1)
	last := strings.ToUpper(raw[len(raw)-1:])
	switch last {
	case "K":
		mult = 1_000
		raw = raw[:len(raw)-1]
	case "M":
		mult = 1_000_000
		raw = raw[:len(raw)-1]
	case "B":
		mult = 1_000_000_000
		raw = raw[:len(raw)-1]
	}
	// Remove separadores. "1,234" → "1234"; "1.2" → "1.2" (decimal).
	if strings.Contains(raw, ".") && strings.Contains(raw, ",") {
		// EN-US style: thousands ',', decimal '.'. Drop commas.
		raw = strings.ReplaceAll(raw, ",", "")
	} else if strings.Count(raw, ",") > 1 || strings.Count(raw, ".") > 1 {
		// Múltiplos separadores → thousands. Remove ambos.
		raw = strings.ReplaceAll(raw, ".", "")
		raw = strings.ReplaceAll(raw, ",", "")
	} else if strings.Contains(raw, ",") && mult > 1 {
		// "1,2K" pt-BR — vira decimal.
		raw = strings.ReplaceAll(raw, ",", ".")
	} else {
		raw = strings.ReplaceAll(raw, ",", "")
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return int64(f * float64(mult))
	}
	return 0
}
