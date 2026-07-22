package http

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Page é a janela pedida pelo cliente numa listagem.
//
// O quê: limite validado + posição do cursor keyset (created_at, id).
// Onde:  produzido por parsePage() nos handlers de listagem admin; consumido
//
//	pelos repositórios, que traduzem em `WHERE (created_at, id) < (...)`.
//
// Efeitos: nenhum — valor puro.
type Page struct {
	// Limit é sempre 1..maxPageLimit; nunca zero.
	Limit int
	// CursorTime/CursorID delimitam a página anterior. Zero-value = primeira
	// página (sem WHERE de posição).
	CursorTime time.Time
	CursorID   string
	// Query é o termo de busca já trimado (pode ser vazio).
	Query string
}

// HasCursor diz se a página começa depois de um item específico.
//
// O quê: distingue "primeira página" de "continuação".
// Onde:  usado pelos repositórios pra decidir se aplicam o predicado keyset.
// Saídas: true quando há posição de cursor válida.
// Efeitos: nenhum.
func (p Page) HasCursor() bool {
	return !p.CursorTime.IsZero() && p.CursorID != ""
}

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// parsePage lê `limit`, `cursor` e `q` da query string, com defaults sãos.
//
// O quê: transforma parâmetros crus (hostis por definição) numa Page válida.
// Onde:  chamada no começo de todo handler de listagem admin paginada.
// Fluxo: vem da URL da request → devolve Page pro handler passar ao repositório.
// Entradas: `r` (request; só a query string é lida).
// Saídas: Page válida e erro quando o cursor é malformado — cursor inválido é
//
//	400, não uma listagem silenciosamente errada.
//
// Efeitos: nenhum — não escreve resposta nem toca estado.
//
// `limit` fora de faixa é CLAMPADO, não rejeitado: um admin digitando 1000 na
// URL quer "o máximo", e devolver 400 aí só atrapalha. Já cursor corrompido é
// erro de verdade — significaria devolver a página errada.
func parsePage(r *http.Request) (Page, error) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("q"))
	// Null byte e control chars não existem em nome/e-mail de gente. Chegando
	// no Postgres, `\x00` estoura "invalid byte sequence for encoding UTF8" e
	// vira 500 — e 500 por input hostil é falha de pentest (§22.8). Rejeita na
	// borda, com mensagem que não ecoa o payload.
	if strings.ContainsFunc(search, func(r rune) bool { return r == 0 || (r < 0x20 && r != '\t') }) {
		return Page{}, fmt.Errorf("busca inválida: contém caractere de controle")
	}
	p := Page{Limit: defaultPageLimit, Query: search}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			// Estouro de faixa (`limit=99999999999999999999`) é intenção clara de
			// "o máximo" — clampa. Já erro de SINTAXE (`abc`, `12.5`, `1e3`) é
			// input malformado e vira 400: aceitar viraria coerção silenciosa,
			// que é justamente o que o pentest de entrada proíbe.
			if !errors.Is(err, strconv.ErrRange) {
				return Page{}, fmt.Errorf("limit inválido: %q não é inteiro", raw)
			}
			n = maxPageLimit
		}
		switch {
		case n < 1:
			p.Limit = 1
		case n > maxPageLimit:
			p.Limit = maxPageLimit
		default:
			p.Limit = n
		}
	}

	if raw := q.Get("cursor"); raw != "" {
		t, id, err := decodeCursor(raw)
		if err != nil {
			return Page{}, err
		}
		p.CursorTime, p.CursorID = t, id
	}
	return p, nil
}

// encodeCursor serializa a posição do último item da página.
//
// O quê: empacota (created_at, id) num token opaco base64.
// Onde:  usado por writePage() pra montar o `next_cursor` da resposta.
// Entradas: `t` (created_at do último item); `id` (UUID do último item).
// Saídas: string base64 URL-safe.
// Efeitos: nenhum.
//
// Opaco de propósito: o cliente não deve construir cursor à mão, senão o
// formato interno vira contrato público e não pode mais mudar.
func encodeCursor(t time.Time, id string) string {
	raw := t.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor desfaz encodeCursor, validando o formato.
//
// O quê: extrai (created_at, id) de um token de cursor.
// Onde:  auxiliar de parsePage.
// Entradas: `raw` (token vindo da query string — não confiável).
// Saídas: instante + id, ou erro se o token não for base64 válido, não tiver os
//
//	dois campos, ou a data não parsear.
//
// Efeitos: nenhum.
func decodeCursor(raw string) (time.Time, string, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("cursor inválido: não é base64")
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", fmt.Errorf("cursor inválido: formato inesperado")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("cursor inválido: timestamp ilegível")
	}
	return t, parts[1], nil
}

// PageMeta acompanha a lista na resposta e diz como pedir a próxima página.
//
// O quê: `next_cursor`/`has_more` (§12) mais o total, que a UI admin usa pra
//
//	mostrar "N clientes" sem varrer todas as páginas.
//
// Onde:  serializado por writePage() em toda listagem admin paginada.
// Efeitos: nenhum — struct de saída.
type PageMeta struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
	Total      int    `json:"total"`
	Limit      int    `json:"limit"`
}

// writePage responde uma listagem paginada no envelope padrão da casa.
//
// O quê: escreve `{data: [...], meta: {next_cursor, has_more, total, limit}}`.
// Onde:  usado pelos handlers de listagem admin no lugar de writeData.
// Fluxo: vem do repositório (itens + total) → vira JSON pro backoffice.
// Entradas: `w`; `items` (a página); `meta` (já montada pelo handler).
// Efeitos: escreve na resposta HTTP.
//
// `data` continua sendo a lista pura — clientes antigos que só liam `data`
// seguem funcionando; `meta` é aditivo.
func writePage(w http.ResponseWriter, items interface{}, meta PageMeta) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": items,
		"meta": meta,
	})
}
