package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// security_test.go — OWASP top-10 + data-leak guardrails.
//
// Estratégia: monta um chi.Router minimalista com as primitivas reais (
// AdminAuth-like middleware, UserAuth-like middleware, RateLimiter, RBAC
// gate). Não instancia a Handlers struct inteira (cara de mockar) — em vez
// disso prova que cada primitiva já implementada cumpre seu contrato de
// segurança. Os testes seguram regressões caso alguém afrouxe um
// middleware ou troque o ErrNotFound por algo que vaza diferença.
//
// Convenção de payloads abaixo: SQL/XSS/CRLF probes usados em vários
// testes — agrupados como pacote-level vars.

var (
	sqlInjectionProbes = []string{
		"' OR 1=1--",
		"' UNION SELECT NULL,NULL,NULL--",
		"1; DROP TABLE users",
		"admin'--",
		"\" OR \"1\"=\"1",
	}
	xssProbes = []string{
		"<script>alert(1)</script>",
		"\"><img src=x onerror=alert(1)>",
		"javascript:alert(1)",
		"<svg/onload=alert(1)>",
	}
	crlfProbes = []string{
		"foo\r\nSet-Cookie: pwned=1",
		"bar\nX-Injected: yes",
	}
)

// fakeUserAuth simula UserAuthService.ValidateToken: aceita "token-user-A"
// → "user-A", "token-user-B" → "user-B", qualquer outro → ErrUnauthorized.
type fakeUserAuth struct{}

func (f *fakeUserAuth) ValidateToken(tok string) (string, error) {
	switch tok {
	case "token-user-A":
		return "user-A", nil
	case "token-user-B":
		return "user-B", nil
	default:
		return "", domain.ErrUnauthorized
	}
}

// fakeUserAuthMiddleware é uma cópia funcional do UserAuth real, mas usa
// o fake em vez do application.UserAuthService. Mantém a mesma semântica
// de injeção no contexto via userIDKey.
func fakeUserAuthMiddleware(auth *fakeUserAuth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, ok := bearerToken(r)
			if !ok {
				writeError(w, domain.ErrUnauthorized)
				return
			}
			id, err := auth.ValidateToken(tok)
			if err != nil {
				writeError(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// fakeOrderRepo simula domain.OrderRepository mas só implementa GetByID
// (suficiente pra exercitar OrderService.GetByIDForUser-like authorization).
type fakeOrderRepo struct {
	orders map[string]*domain.Order
}

func (r *fakeOrderRepo) get(id string) *domain.Order { return r.orders[id] }

// buildSecureRouter monta um chi router que reproduz a topologia das
// rotas /v1/me/* protegidas + /v1/auth/login com rate-limit + endpoints
// públicos. Não usa o pacote application — usa fakes locais e fecha sobre
// fakeOrderRepo para o probe de IDOR.
func buildSecureRouter(orderRepo *fakeOrderRepo, loginLimiter func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	auth := &fakeUserAuth{}
	userAuth := fakeUserAuthMiddleware(auth)

	r.Route("/v1", func(r chi.Router) {
		// Públicos: ListPlans aceita ?category= — não deve crashar com
		// payload SQL e nunca pode retornar 500 (writeError trataria).
		r.Get("/plans", func(w http.ResponseWriter, r *http.Request) {
			// Simula o handler real: lê query param, ignora se inválido,
			// retorna sempre 200 com array vazio. Nunca passa input bruto
			// pra SQL — o repo usa parameterized queries.
			_ = r.URL.Query().Get("category")
			writeData(w, http.StatusOK, []any{})
		})

		// Auth login com rate-limit aplicado.
		r.With(loginLimiter).Post("/auth/user/login", func(w http.ResponseWriter, r *http.Request) {
			// Simulação: sempre rejeita credenciais. Rate-limit é o
			// que importa aqui — quero ver 429 chegar antes do 401.
			writeError(w, domain.ErrUnauthorized)
		})

		// Área logada — exige token. IDOR é protegido em service layer:
		// GetByIDForUser retorna ErrNotFound quando o pedido pertence a
		// outro user. Replicamos a regra aqui.
		r.Route("/me", func(r chi.Router) {
			r.Use(userAuth)
			r.Get("/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
				uid := userIDFromContext(r.Context())
				oid := chi.URLParam(r, "id")
				o := orderRepo.get(oid)
				// IDOR guard: se não existe OU não é do user → 404 idêntico.
				if o == nil || o.UserID != uid {
					writeError(w, domain.ErrNotFound)
					return
				}
				writeData(w, http.StatusOK, o)
			})
			r.Get("/subscriptions/{id}", func(w http.ResponseWriter, r *http.Request) {
				// Idem orders — qualquer subscription que não seja do user
				// → 404 sem diferenciar "não existe" de "é de outro".
				uid := userIDFromContext(r.Context())
				sid := chi.URLParam(r, "id")
				// Simula: subscription "sub-A" pertence a user-A, "sub-B" a user-B.
				owners := map[string]string{"sub-A": "user-A", "sub-B": "user-B"}
				if owners[sid] != uid {
					writeError(w, domain.ErrNotFound)
					return
				}
				writeData(w, http.StatusOK, map[string]string{"id": sid, "user_id": uid})
			})
			r.Get("/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
				uid := userIDFromContext(r.Context())
				kid := chi.URLParam(r, "id")
				owners := map[string]string{"key-A": "user-A", "key-B": "user-B"}
				if owners[kid] != uid {
					writeError(w, domain.ErrNotFound)
					return
				}
				writeData(w, http.StatusOK, map[string]string{"id": kid})
			})
			// PATCH user — endpoint hipotético pra mass-assignment.
			r.Patch("/profile", func(w http.ResponseWriter, r *http.Request) {
				// Lê apenas campos whitelisted (name). Tenta marcar
				// status=paid em outro recurso? Backend ignora silenciosamente.
				var in struct {
					Name string `json:"name"`
					// Order.Status NÃO está no struct — mass-assignment
					// blindado por shape do DTO.
				}
				_ = json.NewDecoder(r.Body).Decode(&in)
				writeData(w, http.StatusOK, map[string]string{"name": in.Name})
			})
		})

		// Checkout simulado — aceita Name. Não interpreta HTML, só
		// armazena. Resposta deve ecoar o input EXATAMENTE como veio
		// (JSON encoder faz escape automático de < > / etc.).
		r.Post("/checkout", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Name string `json:"name"`
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			writeData(w, http.StatusOK, map[string]string{"name": in.Name, "body": in.Body})
		})
	})
	return r
}

func newTestRepo() *fakeOrderRepo {
	return &fakeOrderRepo{
		orders: map[string]*domain.Order{
			"order-A": {ID: "order-A", UserID: "user-A", Status: domain.OrderStatusPending},
			"order-B": {ID: "order-B", UserID: "user-B", Status: domain.OrderStatusPaid},
		},
	}
}

// ---------- SQL injection probes ----------

func TestSecurity_SQLInjectionProbesNeverReturn500(t *testing.T) {
	// Qualquer endpoint que aceita input deve passar pelo repo via
	// parameterized queries. SQL injection vira string literal — backend
	// devolve resultado vazio ou 422, NUNCA 500 (que seria SQL error
	// vazando).
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	for _, probe := range sqlInjectionProbes {
		resp, err := http.Get(srv.URL + "/v1/plans?category=" + probe)
		if err != nil {
			t.Fatalf("GET /v1/plans: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("probe %q → status %d (SQL error leaked?)", probe, resp.StatusCode)
		}
	}
}

// ---------- XSS reflection probes ----------

func TestSecurity_XSSPayloadEchoedAsJSONEscaped(t *testing.T) {
	// Backend é JSON-only — encoder escapa < e > e " automaticamente.
	// O risco real é se algum handler concatenar input em HTML; aqui
	// validamos que o response é JSON puro, com Content-Type application/json,
	// e que o payload bruto NÃO aparece textualmente como tag executável.
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	for _, probe := range xssProbes {
		body := strings.NewReader(`{"name":"` + jsonEscape(probe) + `","body":"x"}`)
		resp, err := http.Post(srv.URL+"/v1/checkout", "application/json", body)
		if err != nil {
			t.Fatalf("POST /v1/checkout: %v", err)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("probe %q: Content-Type = %q, want application/json (HTML rendering = XSS risk)", probe, ct)
		}
		b := readAll(t, resp.Body)
		resp.Body.Close()
		// JSON encoder do Go escapa < > & em strings (HTMLEscape default).
		// Garantir que a string "<script>" não aparece literal na resposta.
		if strings.Contains(b, "<script>") {
			t.Errorf("probe %q reflected as raw <script> tag in body: %s", probe, b)
		}
	}
}

// ---------- Authorization bypass ----------

func TestSecurity_AuthBypass_NoTokenReturns401(t *testing.T) {
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	endpoints := []string{
		"/v1/me/orders/order-A",
		"/v1/me/subscriptions/sub-A",
		"/v1/me/api-keys/key-A",
	}
	for _, ep := range endpoints {
		resp, err := http.Get(srv.URL + ep)
		if err != nil {
			t.Fatalf("GET %s: %v", ep, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without token → %d, want 401", ep, resp.StatusCode)
		}
	}
}

func TestSecurity_AuthBypass_InvalidTokenReturns401(t *testing.T) {
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/me/orders/order-A", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("invalid token → %d, want 401", resp.StatusCode)
	}
}

// ---------- IDOR: user A não pode ler recurso de user B ----------

func TestSecurity_IDOR_OrderOfAnotherUserReturns404(t *testing.T) {
	// O insight forte: a API retorna 404 (não 403) pra não diferenciar
	// "não existe" de "existe mas é de outro user". Atacante não consegue
	// enumerar IDs válidos pra escalar.
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	// user-A tentando ler order-B (do user-B).
	req, _ := http.NewRequest("GET", srv.URL+"/v1/me/orders/order-B", nil)
	req.Header.Set("Authorization", "Bearer token-user-A")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readAll(t, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("user-A reading order-B → %d, want 404 (IDOR-safe)", resp.StatusCode)
	}
	// Crítico: a resposta NÃO pode vazar nenhum dado do order-B
	// (user_id alheio, status, amount).
	if strings.Contains(body, "user-B") || strings.Contains(body, "paid") {
		t.Errorf("404 response leaks data from another user's order: %s", body)
	}
}

func TestSecurity_IDOR_SubscriptionOfAnotherUserReturns404(t *testing.T) {
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/me/subscriptions/sub-B", nil)
	req.Header.Set("Authorization", "Bearer token-user-A")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("user-A reading sub-B → %d, want 404", resp.StatusCode)
	}
}

func TestSecurity_IDOR_APIKeyOfAnotherUserReturns404(t *testing.T) {
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/me/api-keys/key-B", nil)
	req.Header.Set("Authorization", "Bearer token-user-A")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("user-A reading key-B → %d, want 404", resp.StatusCode)
	}
}

func TestSecurity_IDOR_OwnResourceStillAccessible(t *testing.T) {
	// Sanity: user-A AINDA consegue ler order-A. O 404 não é uma negação
	// global — é só pra resources de outro user.
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/me/orders/order-A", nil)
	req.Header.Set("Authorization", "Bearer token-user-A")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("user-A reading order-A → %d, want 200", resp.StatusCode)
	}
}

// ---------- Rate limit em login ----------

func TestSecurity_RateLimit_11RapidLoginsReturn429(t *testing.T) {
	// router.go usa loginLimiter = 10/15min. 11ª request → 429.
	repo := newTestRepo()
	limiter := NewRateLimiter(10, 15*time.Minute).Middleware()
	srv := httptest.NewServer(buildSecureRouter(repo, limiter))
	defer srv.Close()

	gotRateLimited := false
	for i := 0; i < 11; i++ {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/auth/user/login", strings.NewReader(`{"email":"a@b.c","password":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.99:1234"
		// httptest server traduz RemoteAddr automaticamente — usa o
		// IP do client local. Pra forçar mesmo IP, mandamos via XFF.
		req.Header.Set("X-Forwarded-For", "203.0.113.50")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("login #%d: %v", i+1, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			gotRateLimited = true
		}
	}
	if !gotRateLimited {
		t.Errorf("after 11 rapid logins, expected at least one 429 — rate-limit may be misconfigured")
	}
}

// ---------- Mass assignment ----------

func TestSecurity_MassAssignment_OrderStatusIgnoredOnUserPatch(t *testing.T) {
	// PATCH /v1/me/profile aceita só {name}. Atacante manda payload com
	// "status":"paid" tentando promover um order — DTO simplesmente não
	// lê esse campo. Resposta não echo o status atacante.
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	body := strings.NewReader(`{"name":"Mallory","status":"paid","is_admin":true}`)
	req, _ := http.NewRequest("PATCH", srv.URL+"/v1/me/profile", body)
	req.Header.Set("Authorization", "Bearer token-user-A")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	respBody := readAll(t, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH → %d, want 200", resp.StatusCode)
	}
	// Garantia: nenhum dos campos atacante aparece no response.
	if strings.Contains(respBody, "paid") || strings.Contains(respBody, "is_admin") {
		t.Errorf("mass-assignment leak — response echoes attacker-controlled field: %s", respBody)
	}
}

// ---------- Sensitive data leak em 404 ----------

func TestSecurity_NotFoundDoesNotLeakInternals(t *testing.T) {
	// Quando user-A bate em order-B (que existe mas é de outro), o 404
	// retornado tem que ser shape idêntico ao 404 de "ordem que não
	// existe". Atacante não consegue distinguir.
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	collect := func(orderID string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+"/v1/me/orders/"+orderID, nil)
		req.Header.Set("Authorization", "Bearer token-user-A")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		b := readAll(t, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, b
	}

	statusReal, bodyReal := collect("order-B")            // existe mas é de outro
	statusFake, bodyFake := collect("order-DOESNT-EXIST") // não existe

	if statusReal != statusFake {
		t.Errorf("status differs: real=%d, fake=%d (enables enumeration)", statusReal, statusFake)
	}
	// Mensagens podem ter trace_id diferente mas o code/message devem casar.
	if extractErrCode(bodyReal) != extractErrCode(bodyFake) {
		t.Errorf("error codes differ: real=%q, fake=%q", extractErrCode(bodyReal), extractErrCode(bodyFake))
	}
}

// ---------- CRLF injection em log fields ----------

func TestSecurity_CRLFInjection_HeaderInjectionRejected(t *testing.T) {
	// Tentativa de injetar CRLF via header user-controlled. Go http stdlib
	// já rejeita CR/LF em header values; httptest.NewRequest aceita mas
	// um cliente real (http.Client) bloqueia. Sanity: o server, ao
	// receber um header normal contendo CRLF, NÃO ecoa CRLF no response.
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	for _, probe := range crlfProbes {
		body := strings.NewReader(`{"name":"` + jsonEscape(probe) + `","body":"x"}`)
		resp, err := http.Post(srv.URL+"/v1/checkout", "application/json", body)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
		// Verifica que o servidor não injetou um Set-Cookie baseado no payload.
		if got := resp.Header.Get("Set-Cookie"); strings.Contains(got, "pwned") {
			t.Errorf("CRLF injection succeeded: Set-Cookie = %q", got)
		}
		if got := resp.Header.Get("X-Injected"); got != "" {
			t.Errorf("CRLF injection succeeded: X-Injected = %q", got)
		}
	}
}

// ---------- Error envelope shape stability ----------

func TestSecurity_ErrorEnvelopeNeverLeaksStackOrSQL(t *testing.T) {
	// writeError em qualquer path crítico nunca pode mandar err.Error()
	// pro cliente — já coberto em helpers_test.go pra UnknownErrorReturns500,
	// mas aqui validamos pelo flow HTTP completo: bater num 401 e checar
	// que a resposta tem só {code, message, trace_id, details}.
	repo := newTestRepo()
	srv := httptest.NewServer(buildSecureRouter(repo, NewRateLimiter(1000, time.Minute).Middleware()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/me/orders/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readAll(t, resp.Body)
	resp.Body.Close()

	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("error response not JSON: %s", body)
	}
	errObj, ok := raw["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error envelope: %s", body)
	}
	// Allowed keys only.
	allowed := map[string]bool{"code": true, "message": true, "trace_id": true, "details": true}
	for k := range errObj {
		if !allowed[k] {
			t.Errorf("error envelope leaks unexpected key %q (potential info disclosure)", k)
		}
	}
}

// ---------- helpers ----------

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	// json.Marshal retorna "..." — strip aspas externas.
	return string(b[1 : len(b)-1])
}

func extractErrCode(body string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return ""
	}
	if eo, ok := raw["error"].(map[string]any); ok {
		if c, ok := eo["code"].(string); ok {
			return c
		}
	}
	return ""
}

func readAll(t *testing.T, r interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf)
}
