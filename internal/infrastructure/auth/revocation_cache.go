// Package auth — defense-in-depth de revogação de JWT (camada 2).
//
// O dispatcher (viralefy_api_rust) já mantém um hot-set de jtis revogados
// e bloqueia requests revogadas em ~80ms. Esta cache espelha o mesmo
// padrão dentro do core: mesmo se um cliente bater direto no core
// (loopback, debugging, futuro mesh) sem passar pelo dispatcher, o
// token revogado é rejeitado em ValidateToken/ValidateAdmin.
//
// Estratégia:
//
//  1. Bootstrap: SELECT jti FROM revoked_jtis WHERE expires_at > NOW()
//     popula o set in-memory no startup.
//  2. LISTEN/NOTIFY: assina o canal `revoked_jtis_inserted` (emitido pelo
//     handler de revogação ao inserir uma row). Atualiza o set em ms.
//  3. Polling fallback: rebootstrap a cada 30s pra cobrir caso a conexão
//     LISTEN tenha caído entre reconnects.
//  4. Cleanup: a cada 1min, dropa jtis cujo expires_at já passou — evita
//     que a cache cresça indefinidamente.
//
// Lookup IsRevoked() é O(1) com RWMutex — ZERO latência adicional em
// rotas autenticadas (apenas map lookup + read lock).
//
// Tolerante a falha: se a conexão com Postgres cai, o cache continua
// servindo o último estado conhecido + tenta reconectar. Se a inicialização
// falha (sem DB), main.go segue sem cache — dispatcher continua sendo a
// camada primária.
package auth

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RevocationCache mantém em memória o conjunto de JTIs revogados que
// ainda não expiraram. Concurrent-safe.
type RevocationCache struct {
	pool *pgxpool.Pool

	mu sync.RWMutex
	// set[jti] -> expiresAt. Guardar o expires_at permite cleanup local
	// sem round-trip ao DB.
	set map[string]time.Time

	// logger callback — main.go injeta slog. Wrapper genérico pra evitar
	// dependência circular em infrastructure.
	logf func(level, msg string, kv ...any)
}

// LogFunc — assinatura mínima compatível com slog.Logger (Info/Warn).
// level é "info" | "warn" | "error".
type LogFunc func(level, msg string, kv ...any)

// New cria a cache e faz o bootstrap inicial. Não dispara as goroutines
// de LISTEN/polling/cleanup — chame Start para isso. Permite testar
// bootstrap isoladamente.
func New(ctx context.Context, pool *pgxpool.Pool, log LogFunc) (*RevocationCache, error) {
	if log == nil {
		log = func(string, string, ...any) {}
	}
	c := &RevocationCache{
		pool: pool,
		set:  make(map[string]time.Time),
		logf: log,
	}
	if err := c.bootstrap(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Start dispara as 3 goroutines de manutenção. Para no ctx.Done().
//
// Goroutines:
//   - listenLoop: LISTEN/NOTIFY real-time. Reconecta com backoff em erro.
//   - pollLoop: rebootstrap a cada pollInterval (segurança contra LISTEN
//     down silencioso).
//   - cleanupLoop: limpa jtis expirados do set local a cada cleanInterval.
func (c *RevocationCache) Start(ctx context.Context) {
	go c.listenLoop(ctx)
	go c.pollLoop(ctx, 30*time.Second)
	go c.cleanupLoop(ctx, 1*time.Minute)
}

// IsRevoked devolve true se o jti está no hot-set E ainda não expirou.
// jti vazio retorna false (defesa contra tokens sem claim — esses já
// vão falhar em outras checagens upstream).
func (c *RevocationCache) IsRevoked(jti string) bool {
	if jti == "" {
		return false
	}
	c.mu.RLock()
	exp, ok := c.set[jti]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	// Defesa em profundidade: se cleanupLoop ainda não rodou pra esse
	// jti expirado, ainda rejeita (não conta como "revoked" pra clientes
	// que devem usar refresh) — mas como o token expirado já cai no exp
	// check do JWT, na prática o lookup nem chega aqui.
	if time.Now().After(exp) {
		return false
	}
	return true
}

// Size devolve o tamanho atual do set (útil pra métricas/teste).
func (c *RevocationCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.set)
}

// bootstrap recarrega o set inteiro a partir do DB. Operação idempotente:
// substitui o map atual pelo snapshot do DB sob lock de escrita.
func (c *RevocationCache) bootstrap(ctx context.Context) error {
	rows, err := c.pool.Query(ctx,
		`SELECT jti, expires_at FROM revoked_jtis WHERE expires_at > NOW()`)
	if err != nil {
		return err
	}
	defer rows.Close()

	fresh := make(map[string]time.Time, 256)
	for rows.Next() {
		var jti string
		var exp time.Time
		if err := rows.Scan(&jti, &exp); err != nil {
			return err
		}
		fresh[jti] = exp
	}
	if err := rows.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	c.set = fresh
	n := len(fresh)
	c.mu.Unlock()

	c.logf("info", "revocation_cache bootstrap", "count", n)
	return nil
}

// listenLoop assina o canal `revoked_jtis_inserted`. Cada NOTIFY traz o
// jti como payload — adiciona direto no set sem ir ao DB.
//
// O expires_at não vem no NOTIFY payload, então usamos `NOW() + 24h` como
// upper bound. Isso é seguro: tokens core têm TTL típico < 24h, então o
// jti vai estar no set por todo o tempo de vida possível do token. O
// pollLoop reconcilia o expires_at correto eventualmente.
func (c *RevocationCache) listenLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := c.listenOnce(ctx); err != nil {
			c.logf("warn", "revocation_cache LISTEN loop terminated; restart in 5s",
				"error", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// listenOnce mantém uma conexão dedicada com LISTEN ativo até erro/ctx.
func (c *RevocationCache) listenOnce(ctx context.Context) error {
	conn, err := c.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN revoked_jtis_inserted"); err != nil {
		return err
	}
	c.logf("info", "revocation_cache LISTEN revoked_jtis_inserted active")

	for {
		notif, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if notif == nil {
			continue
		}
		jti := notif.Payload
		if jti == "" {
			continue
		}
		// Upper bound conservador: 24h. pollLoop substitui pelo expires_at
		// real do DB na próxima passada.
		c.mu.Lock()
		c.set[jti] = time.Now().Add(24 * time.Hour)
		c.mu.Unlock()
		c.logf("info", "revocation_cache jti added via NOTIFY", "jti", jti)
	}
}

// pollLoop rebootstrapa a cada interval. Garante consistência mesmo se
// o LISTEN ficou desconectado por algum tempo.
func (c *RevocationCache) pollLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := c.bootstrap(pollCtx); err != nil {
				c.logf("warn", "revocation_cache poll failed", "error", err.Error())
			}
			cancel()
		}
	}
}

// cleanupLoop remove jtis expirados do set local. Sem isso o set cresceria
// indefinidamente em ambientes com muita revogação.
func (c *RevocationCache) cleanupLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.mu.Lock()
			before := len(c.set)
			for jti, exp := range c.set {
				if now.After(exp) {
					delete(c.set, jti)
				}
			}
			removed := before - len(c.set)
			c.mu.Unlock()
			if removed > 0 {
				c.logf("info", "revocation_cache cleanup", "removed", removed, "remaining", before-removed)
			}
		}
	}
}

// Compile-time guard: garante que pgx é importado (evita aviso de unused
// import se o package for refatorado pra usar só pgxpool no futuro).
var _ = pgx.ErrNoRows
