package http

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// RateLimiter token-bucket per-IP in-memory. Para HML/POC. Em PRD trocar
// pra Redis se rodar > 1 instância (estado fica fora do processo).
//
// Limit = N requisições / Window. Bucket é simples: contagem em uma janela
// rolante de Window. Quando contagem >= Limit, devolve 429 com
// `Retry-After: <segundos restantes>`.
//
// Critério: por IP (X-Forwarded-For respeitado quando vem do Caddy). Não
// usamos cookie/session porque queremos proteger forms anônimos.

type RateLimiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	count     int
	windowEnd time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*bucket),
	}
}

// Middleware retorna a func(http.Handler) http.Handler para encadear no chi.
// Usa o IP do client (extraído via clientIP helper em handlers.go).
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if ip == "" {
				next.ServeHTTP(w, r)
				return
			}
			if ok, retry := rl.allow(ip); !ok {
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (rl *RateLimiter) allow(key string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok || now.After(b.windowEnd) {
		rl.buckets[key] = &bucket{count: 1, windowEnd: now.Add(rl.window)}
		// Oportunisticamente limpa entradas expiradas (lazy GC) — barato.
		if len(rl.buckets) > 4096 {
			rl.gc(now)
		}
		return true, 0
	}
	if b.count >= rl.limit {
		return false, time.Until(b.windowEnd)
	}
	b.count++
	return true, 0
}

func (rl *RateLimiter) gc(now time.Time) {
	for k, b := range rl.buckets {
		if now.After(b.windowEnd) {
			delete(rl.buckets, k)
		}
	}
}
