package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestRateLimiter_AllowUnderLimit(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if ok, _ := rl.allow("ip-1"); !ok {
			t.Errorf("request #%d should be allowed under limit", i+1)
		}
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		rl.allow("ip-1")
	}
	ok, retry := rl.allow("ip-1")
	if ok {
		t.Errorf("4th request should be blocked")
	}
	if retry <= 0 || retry > time.Minute {
		t.Errorf("retry = %v, want in (0, 1min]", retry)
	}
}

func TestRateLimiter_IsolatesKeys(t *testing.T) {
	// Limite por IP — esgotar ip-1 não pode afetar ip-2.
	rl := NewRateLimiter(2, time.Minute)
	rl.allow("ip-1")
	rl.allow("ip-1")
	if ok, _ := rl.allow("ip-1"); ok {
		t.Errorf("ip-1 should be exhausted")
	}
	if ok, _ := rl.allow("ip-2"); !ok {
		t.Errorf("ip-2 should be allowed (separate bucket)")
	}
}

func TestRateLimiter_RecoversAfterWindow(t *testing.T) {
	// Janela curta pra exercitar a expiração do bucket.
	rl := NewRateLimiter(1, 10*time.Millisecond)
	if ok, _ := rl.allow("ip-1"); !ok {
		t.Fatal("first should pass")
	}
	if ok, _ := rl.allow("ip-1"); ok {
		t.Fatal("second within window should fail")
	}
	time.Sleep(15 * time.Millisecond)
	if ok, _ := rl.allow("ip-1"); !ok {
		t.Errorf("after window, ip-1 should reset and allow again")
	}
}

func TestRateLimiter_Middleware_Allows200(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := rl.Middleware()(next)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRateLimiter_Middleware_Returns429WithRetryAfter(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := rl.Middleware()(next)

	// Esgota o bucket.
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/checkout", nil)
		r.RemoteAddr = "192.0.2.5:1234"
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}

	// 3ª requisição → 429.
	r := httptest.NewRequest("POST", "/checkout", nil)
	r.RemoteAddr = "192.0.2.5:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
	retry := w.Header().Get("Retry-After")
	if retry == "" {
		t.Fatal("Retry-After header missing")
	}
	secs, err := strconv.Atoi(retry)
	if err != nil {
		t.Fatalf("Retry-After = %q, want integer seconds", retry)
	}
	if secs < 0 || secs > 60 {
		t.Errorf("Retry-After = %d, want in [0, 60]", secs)
	}
}

func TestRateLimiter_Middleware_EmptyIPBypasses(t *testing.T) {
	// Quando clientIP() devolve "" (RemoteAddr e XFF ausentes) — política
	// fail-open: não rate-limita anônimo, melhor que recusar tráfego legítimo.
	rl := NewRateLimiter(1, time.Minute)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := rl.Middleware()(next)

	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = ""
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("request #%d with empty IP = %d, want 200 (bypass)", i, w.Code)
		}
	}
}

func TestRateLimiter_ConcurrentSafetyDoesNotPanic(t *testing.T) {
	// Smoke test de concorrência — mutex deve segurar.
	rl := NewRateLimiter(100, time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ip := "ip-" + strconv.Itoa(n%10)
			for j := 0; j < 20; j++ {
				rl.allow(ip)
			}
		}(i)
	}
	wg.Wait()
}

// ---------- recordingWriter (idempotency support) ----------

func TestRecordingWriter_CapturesStatusAndBody(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &recordingWriter{ResponseWriter: w, body: &bytes.Buffer{}, status: http.StatusOK}

	rec.WriteHeader(201)
	_, _ = rec.Write([]byte(`{"data":42}`))

	if rec.status != 201 {
		t.Errorf("captured status = %d, want 201", rec.status)
	}
	if rec.body.String() != `{"data":42}` {
		t.Errorf("captured body = %q, want JSON payload", rec.body.String())
	}
	// E também propaga para o ResponseWriter real.
	if w.Code != 201 {
		t.Errorf("downstream status = %d, want 201", w.Code)
	}
	if w.Body.String() != `{"data":42}` {
		t.Errorf("downstream body = %q", w.Body.String())
	}
}
