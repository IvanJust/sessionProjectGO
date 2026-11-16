package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"sessionProject/internal/auth"
	"sessionProject/internal/config"
	"sessionProject/internal/database"
	"sessionProject/internal/httpapi"
	"sessionProject/internal/store"
	"sessionProject/migrations"
)

type fixture struct {
	handler  http.Handler
	pool     *pgxpool.Pool
	rdb      *redis.Client
	sessions *store.Sessions
	ip       string
	users    []string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("RUN_INTEGRATION") != "1" {
		t.Skip("set RUN_INTEGRATION=1 with PostgreSQL and Redis running")
	}
	_ = godotenv.Load("../.env")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := database.NewPostgresPool(ctx, cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Running twice also verifies migrations are idempotent.
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rdb, err := database.NewRedisClient(ctx, cfg.Redis)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	sessions := store.NewSessions(rdb)
	svc, err := auth.NewService(store.NewUsers(pool), sessions, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{pool: pool, rdb: rdb, sessions: sessions, ip: "test-" + randomID() + ":12345"}
	f.handler = httpapi.New(svc, store.NewLimiter(rdb), config.SessionConfig{CookieName: "session", CookieSecure: true}, func(ctx context.Context) error { return errors.Join(pool.Ping(ctx), rdb.Ping(ctx).Err()) })
	t.Cleanup(func() {
		for _, id := range f.users {
			if err := sessions.RevokeAll(ctx, id); err != nil {
				t.Error(err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id); err != nil {
				t.Error(err)
			}
		}
	})
	return f
}

func randomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (f *fixture) call(t *testing.T, method, path, body string, cookie *http.Cookie, headers map[string]string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.RemoteAddr = f.ip
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: got %d, want %d; body=%s", method, path, w.Code, status, w.Body.String())
	}
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *fixture) register(t *testing.T, email string) (auth.Identity, *http.Cookie) {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":"correct horse battery staple"}`, email)
	w := f.call(t, "POST", "/api/auth/register", body, nil, nil, 201)
	id := decode[auth.Identity](t, w)
	f.users = append(f.users, id.User.ID)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("registration must set one cookie")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge < 1 {
		t.Fatal("missing session cookie protection")
	}
	if strings.Contains(w.Body.String(), cookie.Value) || strings.Contains(w.Body.String(), "password_hash") {
		t.Fatal("credentials leaked into JSON")
	}
	return id, cookie
}

func TestAPISessionLifecycleAndIsolation(t *testing.T) {
	f := setup(t)
	email := "session-test-" + randomID() + "@example.com"
	f.call(t, "GET", "/api/me", "", nil, nil, 401)
	f.call(t, "GET", "/ready", "", nil, nil, 200)
	first, cookie1 := f.register(t, "  "+strings.ToUpper(email)+"  ")
	if first.User.Email != email {
		t.Fatal("email not normalized")
	}
	wrong := fmt.Sprintf(`{"email":%q,"password":"incorrect-password"}`, email)
	w := f.call(t, "POST", "/api/auth/login", wrong, nil, nil, 401)
	unknown := f.call(t, "POST", "/api/auth/login", `{"email":"unknown@example.com","password":"incorrect-password"}`, nil, nil, 401)
	if w.Body.String() != unknown.Body.String() {
		t.Fatal("unknown user and wrong password must have same response")
	}
	body := fmt.Sprintf(`{"email":%q,"password":"correct horse battery staple"}`, email)
	w = f.call(t, "POST", "/api/auth/login", body, nil, map[string]string{"User-Agent": "Mozilla/5.0 Firefox/140.0"}, 200)
	second := decode[auth.Identity](t, w)
	cookie2 := w.Result().Cookies()[0]
	if cookie1.Value == cookie2.Value {
		t.Fatal("login must generate a new token")
	}
	f.call(t, "GET", "/api/me", "", cookie1, nil, 200)
	f.call(t, "GET", "/api/me", "", cookie2, nil, 200)
	w = f.call(t, "GET", "/api/sessions", "", cookie1, nil, 200)
	list := decode[struct {
		Sessions []auth.Session `json:"sessions"`
	}](t, w).Sessions
	if len(list) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(list))
	}
	current := 0
	for _, s := range list {
		if s.Current {
			current++
		}
	}
	if current != 1 {
		t.Fatal("exactly one session must be marked current")
	}
	_, other := f.register(t, "session-test-"+randomID()+"@example.com")
	f.call(t, "DELETE", "/api/sessions/"+second.Session.ID, "", other, nil, 404)
	f.call(t, "POST", "/api/auth/logout", "", cookie1, map[string]string{"Origin": "https://evil.example"}, 403)
	f.call(t, "POST", "/api/auth/logout-all", "", cookie1, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403)
	f.call(t, "GET", "/api/me", "", cookie1, nil, 200)
	f.call(t, "DELETE", "/api/sessions/"+second.Session.ID, "", cookie1, nil, 204)
	f.call(t, "GET", "/api/me", "", cookie2, nil, 401)
	f.call(t, "GET", "/api/me", "", cookie1, nil, 200)
	f.call(t, "POST", "/api/auth/logout-all", "", cookie1, nil, 204)
	f.call(t, "GET", "/api/me", "", cookie1, nil, 401)
	f.call(t, "GET", "/api/me", "", other, nil, 200)
	f.call(t, "POST", "/api/auth/register", body, nil, nil, 409)
	f.call(t, "POST", "/api/auth/logout", "", cookie1, nil, 204)
	f.call(t, "POST", "/api/auth/logout", "", nil, nil, 204)
	w = f.call(t, "DELETE", "/api/sessions/not-a-session", "", other, nil, 404)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("API errors must not be cached")
	}
}

func TestAPIValidationAndRateLimit(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		body    string
		headers map[string]string
		status  int
	}{
		{`{`, nil, 400},
		{`null`, nil, 400},
		{`{"email":"a@example.com","password":"something","extra":true}`, nil, 400},
		{`{} {}`, nil, 400},
		{`{}`, map[string]string{"Content-Type": "text/plain"}, 415},
		{`{"password":"` + strings.Repeat("x", 17<<10) + `"}`, nil, 413},
	} {
		f.call(t, "POST", "/api/auth/login", tc.body, nil, tc.headers, tc.status)
	}
	for i := 6; i < 20; i++ {
		f.call(t, "POST", "/api/auth/login", "{", nil, nil, 400)
	}
	w := f.call(t, "POST", "/api/auth/login", "{", nil, nil, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("rate limit must include Retry-After")
	}
	f.call(t, "POST", "/api/auth/register", `{"email":"wrong","password":"correct horse battery staple"}`, nil, nil, 400)
	f.call(t, "POST", "/api/auth/register", `{"email":"ok@example.com","password":"short"}`, nil, nil, 400)
	f.call(t, "GET", "/api/unknown", "", nil, nil, 404)
	f.call(t, "PUT", "/api/me", "", nil, nil, 405)
}

func TestSessionLimitExpiryAndInactiveUser(t *testing.T) {
	f := setup(t)
	email := "session-test-" + randomID() + "@example.com"
	id, first := f.register(t, email)
	var latest *http.Cookie
	for i := 0; i < 10; i++ {
		w := f.call(t, "POST", "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":"correct horse battery staple"}`, email), nil, nil, 200)
		latest = w.Result().Cookies()[0]
	}
	w := f.call(t, "GET", "/api/sessions", "", latest, nil, 200)
	list := decode[struct {
		Sessions []auth.Session `json:"sessions"`
	}](t, w).Sessions
	if len(list) != 10 {
		t.Fatalf("want 10 sessions, got %d", len(list))
	}
	f.call(t, "GET", "/api/me", "", first, nil, 401)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, id.User.ID); err != nil {
		t.Fatal(err)
	}
	f.call(t, "GET", "/api/me", "", latest, nil, 401)
	f.call(t, "POST", "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":"correct horse battery staple"}`, email), nil, nil, 401)
	if _, err := f.pool.Exec(ctx, `UPDATE users SET is_active=true WHERE id=$1`, id.User.ID); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	session := auth.Session{ID: hex.EncodeToString(digest[:]), UserID: id.User.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(50 * time.Millisecond)}
	if err := f.sessions.Create(ctx, session, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := f.sessions.Get(ctx, session.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Redis did not expire session: %v", err)
	}
	f.call(t, "GET", "/api/me", "", &http.Cookie{Name: "session", Value: token}, nil, 401)
}

func TestConcurrentSessionCreation(t *testing.T) {
	f := setup(t)
	id, _ := f.register(t, "session-test-"+randomID()+"@example.com")
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			now := time.Now()
			session := auth.Session{ID: randomID() + randomID() + randomID()[:16], UserID: id.User.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
			if err := f.sessions.Create(ctx, session, time.Hour); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := f.sessions.List(ctx, id.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 10 {
		t.Fatalf("concurrent login exceeded session limit: %d", len(list))
	}
	if err := f.sessions.RevokeAll(ctx, id.User.ID); err != nil {
		t.Fatal(err)
	}
	for _, session := range list {
		if _, err := f.sessions.Get(ctx, session.ID); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("logout-all left a session usable")
		}
	}
}

func TestUnavailableRedisFailsClosed(t *testing.T) {
	f := setup(t)
	_, cookie := f.register(t, "session-test-"+randomID()+"@example.com")
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	defer rdb.Close()
	svc, err := auth.NewService(store.NewUsers(f.pool), store.NewSessions(rdb), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = httpapi.New(svc, store.NewLimiter(rdb), config.SessionConfig{CookieName: "session"}, func(context.Context) error { return errors.New("unavailable") })
	f.call(t, "GET", "/api/me", "", cookie, nil, 503)
	f.call(t, "POST", "/api/auth/logout", "", cookie, nil, 503)
	f.call(t, "GET", "/ready", "", nil, nil, 503)
}

func TestFreshDatabaseSchema(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	schema := "session_test_" + randomID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := f.pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.pool.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var idType string
	if err := pool.QueryRow(ctx, `SELECT data_type FROM information_schema.columns WHERE table_schema=$1 AND table_name='users' AND column_name='id'`, schema).Scan(&idType); err != nil {
		t.Fatal(err)
	}
	if idType != "uuid" {
		t.Fatalf("new database should have UUID user IDs, got %s", idType)
	}
	users := store.NewUsers(pool)
	user, err := users.Create(ctx, "fresh@example.com", "unused-test-hash")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := users.ByID(ctx, user.ID)
	if err != nil || loaded.ID != user.ID {
		t.Fatalf("UUID user lookup failed: %v", err)
	}
}
