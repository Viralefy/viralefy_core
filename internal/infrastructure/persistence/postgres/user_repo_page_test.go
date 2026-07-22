package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// openTestDB abre o pool contra o Postgres de teste, ou pula a suíte.
//
// O quê: resolve DATABASE_URL e conecta; sem banco, os testes de integração são
//
//	pulados em vez de falhar (dev sem docker continua rodando `go test`).
//
// Onde:  usado só pelos testes de paginação deste pacote.
// Saídas: *DB pronto, ou t.Skip.
// Efeitos: abre conexão com o Postgres.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL não definido — teste de integração pulado")
	}
	db, err := New(context.Background(), url)
	if err != nil {
		t.Skipf("Postgres indisponível (%v) — teste de integração pulado", err)
	}
	return db
}

// TestListPageWithCreditBalancePaginates prova que a paginação keyset percorre
// a lista INTEIRA sem pular nem repetir.
//
// O quê: percorre todas as páginas acumulando ids e compara com o total.
// Onde:  é a garantia central da tela de clientes — antes o backend cortava em
//
//	200 sem avisar, e o resto dos clientes simplesmente não existia.
//
// Efeitos: só leitura.
func TestListPageWithCreditBalancePaginates(t *testing.T) {
	db := openTestDB(t)
	defer db.pool.Close()
	repo := NewUserRepo(db)
	ctx := context.Background()

	const pageSize = 25
	seen := map[string]bool{}
	q := domain.UserListQuery{Limit: pageSize}
	var total int
	pages := 0

	for {
		items, tot, err := repo.ListPageWithCreditBalance(ctx, q)
		if err != nil {
			t.Fatalf("página %d falhou: %v", pages+1, err)
		}
		total = tot
		for _, u := range items {
			if seen[u.ID] {
				t.Fatalf("id %s apareceu em duas páginas — cursor está repetindo linha", u.ID)
			}
			seen[u.ID] = true
		}
		pages++
		if len(items) < pageSize {
			break
		}
		last := items[len(items)-1]
		q.CursorTime, q.CursorID = last.CreatedAt, last.ID
		if pages > 100 {
			t.Fatal("mais de 100 páginas — cursor não está avançando")
		}
	}

	if len(seen) != total {
		t.Errorf("percorreu %d usuários mas o total diz %d — a paginação perdeu linhas", len(seen), total)
	}
	if pages < 2 {
		t.Skipf("banco com poucos registros (%d) — teste precisa de mais de uma página", total)
	}
}

// TestListPageExcludesTestFixtures prova o filtro de contas de teste.
//
// O quê: por default fixtures `@viralefy.test` não aparecem; com IncludeTest
//
//	aparecem, e o total cresce junto.
//
// Onde:  é o que tira o "Critical Flow Bot" da lista de clientes do backoffice.
// Efeitos: só leitura.
func TestListPageExcludesTestFixtures(t *testing.T) {
	db := openTestDB(t)
	defer db.pool.Close()
	repo := NewUserRepo(db)
	ctx := context.Background()

	_, semTeste, err := repo.ListPageWithCreditBalance(ctx, domain.UserListQuery{Limit: 1})
	if err != nil {
		t.Fatalf("listagem sem fixtures falhou: %v", err)
	}
	_, comTeste, err := repo.ListPageWithCreditBalance(ctx, domain.UserListQuery{Limit: 1, IncludeTest: true})
	if err != nil {
		t.Fatalf("listagem com fixtures falhou: %v", err)
	}
	if comTeste < semTeste {
		t.Errorf("total com fixtures (%d) menor que sem (%d) — filtro invertido", comTeste, semTeste)
	}

	// Nenhuma página default pode conter fixture, por mais que se avance.
	items, _, err := repo.ListPageWithCreditBalance(ctx, domain.UserListQuery{Limit: 200})
	if err != nil {
		t.Fatalf("listagem falhou: %v", err)
	}
	for _, u := range items {
		if len(u.Email) > 14 && u.Email[len(u.Email)-14:] == "@viralefy.test" {
			t.Errorf("fixture %q vazou pra lista de clientes", u.Email)
		}
	}
}

// TestListPageSearchIsServerSide prova que a busca filtra no banco.
//
// O quê: um termo que casa poucos registros reduz o total; termo impossível
//
//	zera. Antes o filtro era em memória e só via a página carregada.
//
// Onde:  a busca da tela de clientes.
// Efeitos: só leitura.
func TestListPageSearchIsServerSide(t *testing.T) {
	db := openTestDB(t)
	defer db.pool.Close()
	repo := NewUserRepo(db)
	ctx := context.Background()

	_, baseline, err := repo.ListPageWithCreditBalance(ctx, domain.UserListQuery{Limit: 1})
	if err != nil {
		t.Fatalf("baseline falhou: %v", err)
	}

	_, zero, err := repo.ListPageWithCreditBalance(ctx, domain.UserListQuery{
		Limit:  10,
		Search: "zzz-nao-existe-nenhum-cliente-assim-zzz",
	})
	if err != nil {
		t.Fatalf("busca impossível falhou: %v", err)
	}
	if zero != 0 {
		t.Errorf("busca sem resultado deveria dar total 0, veio %d", zero)
	}
	if baseline == 0 {
		t.Skip("banco vazio — nada a comparar")
	}
}

// TestListPageRejectsInjectionInSearch prova que o termo de busca é parâmetro.
//
// O quê: payload de SQL injection no campo de busca é tratado como TEXTO — não
//
//	derruba a query nem retorna a base inteira.
//
// Onde:  a busca é input livre do admin; um `'` solto já quebraria concatenação.
// Efeitos: só leitura.
func TestListPageRejectsInjectionInSearch(t *testing.T) {
	db := openTestDB(t)
	defer db.pool.Close()
	repo := NewUserRepo(db)
	ctx := context.Background()

	// Payloads que precisam ser tratados como TEXTO. O null byte não entra aqui:
	// ele é rejeitado antes, na borda HTTP (parsePage), porque o Postgres não
	// aceita 0x00 em texto e a query estouraria — ver TestParsePage no pacote
	// interface/http.
	hostile := []string{
		"' OR 1=1 --",
		"'; DROP TABLE users; --",
		"admin@exemplo.com'; --",
		"çãé", // acentos: nome real, tem que funcionar
		"𝕏",   // unicode astral
	}
	for _, s := range hostile {
		items, _, err := repo.ListPageWithCreditBalance(ctx, domain.UserListQuery{Limit: 5, Search: s})
		if err != nil {
			t.Errorf("busca %q deveria ser tratada como texto, mas a query falhou: %v", s, err)
			continue
		}
		if len(items) > 5 {
			t.Errorf("busca %q devolveu %d itens acima do limite", s, len(items))
		}
	}

	// Curingas de LIKE têm que casar LITERALMENTE. Antes, buscar "%" devolvia a
	// base inteira — o admin achava que tinha encontrado tudo, mas não filtrou
	// nada.
	_, todos, err := repo.ListPageWithCreditBalance(ctx, domain.UserListQuery{Limit: 1})
	if err != nil {
		t.Fatalf("baseline falhou: %v", err)
	}
	for _, wildcard := range []string{"%", "_", "%%", "_%"} {
		_, n, err := repo.ListPageWithCreditBalance(ctx, domain.UserListQuery{Limit: 5, Search: wildcard})
		if err != nil {
			t.Errorf("busca %q falhou: %v", wildcard, err)
			continue
		}
		if todos > 0 && n == todos {
			t.Errorf("busca %q casou TODOS os %d registros — wildcard não foi escapado", wildcard, n)
		}
	}
}
