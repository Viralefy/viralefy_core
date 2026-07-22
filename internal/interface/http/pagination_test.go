package http

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestParsePageDefaults garante que ausência de parâmetro vira página válida.
//
// O quê: sem query string, parsePage devolve o limite default e nenhum cursor.
// Onde:  protege o caminho mais comum (primeira abertura da tela de clientes).
func TestParsePageDefaults(t *testing.T) {
	p, err := parsePage(httptest.NewRequest("GET", "/v1/admin/users", nil))
	if err != nil {
		t.Fatalf("sem parâmetro deveria ser válido, veio erro: %v", err)
	}
	if p.Limit != defaultPageLimit {
		t.Errorf("limit default = %d, esperado %d", p.Limit, defaultPageLimit)
	}
	if p.HasCursor() {
		t.Error("sem cursor na URL, HasCursor() deveria ser false")
	}
	if p.Query != "" {
		t.Errorf("query deveria ser vazia, veio %q", p.Query)
	}
}

// TestParsePageLimitBoundaries cobre as bordas do clamp de `limit`.
//
// O quê: valor fora de faixa é ajustado, não rejeitado; lixo é rejeitado.
// Onde:  é a diferença entre um admin curioso na URL e um input hostil.
func TestParsePageLimitBoundaries(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"1", 1, false},
		{"0", 1, false},                                 // abaixo do mínimo → clamp
		{"-5", 1, false},                                // negativo → clamp
		{"200", maxPageLimit, false},                    // no teto
		{"201", maxPageLimit, false},                    // acima → clamp
		{"999999", maxPageLimit, false},                 // muito acima → clamp
		{"abc", 0, true},                                // não-numérico → 400
		{"1e3", 0, true},                                // notação científica → 400
		{"12.5", 0, true},                               // float → 400
		{"%2010", 0, true},                              // espaço encodado → 400 (sem coerção)
		{strings.Repeat("9", 400), maxPageLimit, false}, // estouro de faixa → clamp, não panic nem 400
	}
	for _, c := range cases {
		p, err := parsePage(httptest.NewRequest("GET", "/v1/admin/users?limit="+c.raw, nil))
		if c.wantErr {
			if err == nil {
				t.Errorf("limit=%q deveria falhar, veio limit=%d", c.raw, p.Limit)
			}
			continue
		}
		if err != nil {
			t.Errorf("limit=%q deveria passar, veio erro: %v", c.raw, err)
			continue
		}
		if p.Limit != c.want {
			t.Errorf("limit=%q → %d, esperado %d", c.raw, p.Limit, c.want)
		}
	}
}

// TestCursorRoundTrip prova que encode→decode preserva a posição.
//
// O quê: o par (created_at, id) volta idêntico ao nanossegundo.
// Onde:  é a garantia de que a próxima página começa exatamente onde a anterior
//
//	terminou — se o instante truncar, uma linha é pulada ou repetida.
func TestCursorRoundTrip(t *testing.T) {
	want := time.Date(2026, 7, 21, 15, 4, 5, 123456789, time.UTC)
	id := "0198c0de-0000-7000-8000-00000000abcd"

	gotT, gotID, err := decodeCursor(encodeCursor(want, id))
	if err != nil {
		t.Fatalf("round-trip falhou: %v", err)
	}
	if !gotT.Equal(want) {
		t.Errorf("timestamp = %v, esperado %v (precisão perdida?)", gotT, want)
	}
	if gotID != id {
		t.Errorf("id = %q, esperado %q", gotID, id)
	}
}

// TestDecodeCursorRejectsGarbage cobre os cursores hostis.
//
// O quê: token corrompido vira erro, nunca uma posição silenciosamente errada.
// Onde:  cursor vem da URL, então é entrada não confiável por definição. Aceitar
//
//	lixo aqui significaria devolver a página errada sem ninguém perceber.
func TestDecodeCursorRejectsGarbage(t *testing.T) {
	bad := []string{
		"",                       // vazio
		"!!!",                    // não é base64
		encodeRaw("sem-pipe"),    // falta o separador
		encodeRaw("|só-id"),      // timestamp vazio
		encodeRaw("2026-07-21|"), // id vazio
		encodeRaw("ontem|abc"),   // timestamp ilegível
		encodeRaw("|"),           // ambos vazios
	}
	for _, raw := range bad {
		if _, _, err := decodeCursor(raw); err == nil {
			t.Errorf("cursor %q deveria ser rejeitado", raw)
		}
	}
}

// TestParsePageRejectsBadCursor liga a rejeição do cursor ao contrato HTTP.
//
// O quê: cursor inválido faz parsePage falhar (o handler traduz em 400).
// Onde:  garante que a borda não engole o erro e devolve a primeira página como
//
//	se nada tivesse acontecido.
func TestParsePageRejectsBadCursor(t *testing.T) {
	if _, err := parsePage(httptest.NewRequest("GET", "/v1/admin/users?cursor=xxx!!", nil)); err == nil {
		t.Error("cursor malformado deveria falhar, não cair na primeira página")
	}
}

// TestParsePageTrimsQuery cobre a normalização do termo de busca.
//
// O quê: espaços em volta somem; busca só de espaços vira busca vazia.
// Onde:  evita que "  " vire um ILIKE '%  %' que não casa com nada e faz o admin
//
//	achar que não há clientes.
func TestParsePageTrimsQuery(t *testing.T) {
	p, err := parsePage(httptest.NewRequest("GET", "/v1/admin/users?q=%20%20ana%20%20", nil))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if p.Query != "ana" {
		t.Errorf("query = %q, esperado \"ana\"", p.Query)
	}

	blank, err := parsePage(httptest.NewRequest("GET", "/v1/admin/users?q=%20%20%20", nil))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if blank.Query != "" {
		t.Errorf("busca só com espaços deveria virar vazia, veio %q", blank.Query)
	}
}

// encodeRaw é atalho de teste pra montar cursores propositalmente inválidos.
//
// O quê: base64 de uma string arbitrária, sem passar por encodeCursor — é assim
//
//	que se constrói um token com conteúdo quebrado mas encoding válido.
//
// Onde:  só neste arquivo, nos casos hostis de decodeCursor.
func encodeRaw(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// TestParsePageRejectsControlCharsInSearch fecha o buraco que o teste de
// integração escancarou.
//
// O quê: busca com null byte (ou outro control char) é rejeitada na BORDA.
// Onde:  sem isso o termo chegava no Postgres e estourava "invalid byte
//
//	sequence for encoding UTF8" — ou seja, **500 por input hostil**, que é
//	exatamente o que a §22.8 proíbe. Agora é 400.
func TestParsePageRejectsControlCharsInSearch(t *testing.T) {
	hostile := []string{
		"q=%00",            // null byte puro
		"q=abc%00def",      // null no meio
		"q=%01%02",         // outros control chars
		"q=linha%0Aquebra", // newline: injeção de log
	}
	for _, qs := range hostile {
		if _, err := parsePage(httptest.NewRequest("GET", "/v1/admin/users?"+qs, nil)); err == nil {
			t.Errorf("busca %q deveria ser rejeitada na borda", qs)
		}
	}

	// Contraprova: acento e unicode legítimo PASSAM — nome de gente tem acento,
	// e rejeitar isso quebraria a busca pra metade da base.
	for _, qs := range []string{"q=Ana", "q=jo%C3%A3o", "q=%F0%9D%95%8F"} {
		if _, err := parsePage(httptest.NewRequest("GET", "/v1/admin/users?"+qs, nil)); err != nil {
			t.Errorf("busca legítima %q foi rejeitada: %v", qs, err)
		}
	}
}
