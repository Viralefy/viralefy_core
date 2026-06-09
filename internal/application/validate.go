package application

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// Validador server-side. Garante que o usuário não mande lixo no checkout
// (handle quebrado, URL de plataforma errada, etc.) — primeira linha de
// defesa antes de processar pedidos.

var (
	// Instagram: 1–30 chars, letras/dígitos/underscore/ponto. Não pode iniciar
	// nem terminar com ponto, e não pode ter dois pontos seguidos (regra real
	// do IG). Para MVP simplificamos: aceita conforme regex abaixo.
	reInstagramHandle = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.]{0,28}[A-Za-z0-9])?$`)
	// TikTok: 2–24 chars, letras/dígitos/underscore/ponto.
	reTikTokHandle = regexp.MustCompile(`^[A-Za-z0-9_.]{2,24}$`)

	// URLs de publicação.
	reInstagramURL = regexp.MustCompile(`^https?://(?:www\.)?instagram\.com/(?:p|reel|tv)/[A-Za-z0-9_-]+/?(?:\?.*)?$`)
	reTikTokURL    = regexp.MustCompile(`^https?://(?:www\.|m\.)?tiktok\.com/@[^/]+/video/\d+/?(?:\?.*)?$`)
	reTikTokShort  = regexp.MustCompile(`^https?://vm\.tiktok\.com/[A-Za-z0-9]+/?$`)
)

// NormalizeHandle remove @, espaços, lowercase.
func NormalizeHandle(in string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(in), "@")))
}

// ValidateHandle confere se um handle bate com o formato da plataforma.
func ValidateHandle(platform domain.Platform, handle string) error {
	if !platform.IsValid() {
		return fmt.Errorf("plataforma inválida: %q", platform)
	}
	handle = NormalizeHandle(handle)
	if handle == "" {
		return fmt.Errorf("handle vazio")
	}
	switch platform {
	case domain.PlatformInstagram:
		if !reInstagramHandle.MatchString(handle) {
			return fmt.Errorf("handle do Instagram inválido: %q", handle)
		}
	case domain.PlatformTikTok:
		if !reTikTokHandle.MatchString(handle) {
			return fmt.Errorf("handle do TikTok inválido: %q", handle)
		}
	}
	return nil
}

// ValidatePublicationURL confere se uma URL é de publicação real da plataforma.
func ValidatePublicationURL(platform domain.Platform, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("URL vazia")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("URL malformada")
	}
	switch platform {
	case domain.PlatformInstagram:
		if !reInstagramURL.MatchString(raw) {
			return fmt.Errorf("URL não parece ser de uma publicação do Instagram (use /p/, /reel/ ou /tv/)")
		}
	case domain.PlatformTikTok:
		if !reTikTokURL.MatchString(raw) && !reTikTokShort.MatchString(raw) {
			return fmt.Errorf("URL não parece ser de um vídeo do TikTok (formatos aceitos: tiktok.com/@user/video/<id> ou vm.tiktok.com/<id>)")
		}
	default:
		return fmt.Errorf("plataforma inválida: %q", platform)
	}
	return nil
}
