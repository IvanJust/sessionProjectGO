package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"sessionProject/internal/auth"
	"sessionProject/internal/config"
	"sessionProject/internal/web"
)

type RateLimiter interface {
	Allow(context.Context, string, int, time.Duration) (time.Duration, error)
}

type API struct {
	service *auth.Service
	limiter RateLimiter
	cookies config.SessionConfig
	ready   func(context.Context) error
}

func New(service *auth.Service, limiter RateLimiter, cookies config.SessionConfig, ready func(context.Context) error) http.Handler {
	a := &API{service: service, limiter: limiter, cookies: cookies, ready: ready}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, securityHeaders)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/ready", a.readiness)
	r.Route("/api", func(r chi.Router) {
		r.Use(noStore, crossOriginProtection)
		r.Post("/auth/register", a.rateLimit("register", 5, a.register))
		r.Post("/auth/login", a.rateLimit("login", 20, a.login))
		r.Post("/auth/logout", a.logout)
		r.Group(func(r chi.Router) {
			r.Use(a.requireAuth)
			r.Get("/me", a.me)
			r.Get("/sessions", a.sessions)
			r.Delete("/sessions/{id}", a.revoke)
			r.Post("/auth/logout-all", a.logoutAll)
		})
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, 404, "not_found", "Ручка не найдена.")
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, 405, "method_not_allowed", "Метод не поддерживается.")
		})
	})
	r.Handle("/*", web.Handler())
	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func serviceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidEmail):
		writeError(w, 400, "invalid_email", "Укажите корректный email.")
	case errors.Is(err, auth.ErrInvalidPassword):
		writeError(w, 400, "invalid_password", "Пароль должен содержать от 12 до 1024 байт.")
	case errors.Is(err, auth.ErrEmailExists):
		writeError(w, 409, "email_exists", "Этот email уже зарегистрирован. Попробуйте войти.")
	case errors.Is(err, auth.ErrCredentials):
		writeError(w, 401, "invalid_credentials", "Неверный email или пароль.")
	case errors.Is(err, auth.ErrUnauthorized):
		writeError(w, 401, "unauthorized", "Войдите в аккаунт.")
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, 404, "not_found", "Сессия не найдена.")
	default:
		slog.ErrorContext(r.Context(), "API operation failed", "request_id", middleware.GetReqID(r.Context()), "error", err)
		writeError(w, 503, "unavailable", "Сервис временно недоступен. Попробуйте ещё раз.")
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func crossOriginProtection(next http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, 403, "cross_origin", "Запрос с другого сайта запрещён.")
	}))
	return protection.Handler(next)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *API) rateLimit(scope string, limit int, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		delay, err := a.limiter.Allow(r.Context(), scope+":"+clientIP(r), limit, 15*time.Minute)
		if err != nil {
			serviceError(w, r, err)
			return
		}
		if delay > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(delay.Seconds())+1))
			writeError(w, 429, "rate_limited", "Слишком много попыток. Повторите позже.")
			return
		}
		next(w, r)
	}
}

type identityKey struct{}

func identity(r *http.Request) auth.Identity { return r.Context().Value(identityKey{}).(auth.Identity) }

func (a *API) token(r *http.Request) string {
	cookie, err := r.Cookie(a.cookies.CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.service.Authenticate(r.Context(), a.token(r))
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) {
				a.clearCookie(w)
			}
			serviceError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}

func (a *API) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: a.cookies.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: a.cookies.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}

func (a *API) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: a.cookies.CookieName, Value: "", Path: "/", HttpOnly: true, Secure: a.cookies.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var input credentials
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, 415, "unsupported_media_type", "Используйте Content-Type: application/json.")
		return input, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var decoded *credentials
	err = decoder.Decode(&decoded)
	if err == nil && decoded != nil {
		var extra any
		err = decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			return *decoded, true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, 413, "body_too_large", "Тело запроса слишком большое.")
	} else {
		writeError(w, 400, "invalid_json", "Ожидается один JSON-объект с email и password.")
	}
	return input, false
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	input, ok := readCredentials(w, r)
	if !ok {
		return
	}
	id, token, err := a.service.Register(r.Context(), input.Email, input.Password, clientIP(r), r.UserAgent())
	if err != nil {
		serviceError(w, r, err)
		return
	}
	a.setCookie(w, token, id.Session.ExpiresAt)
	writeJSON(w, http.StatusCreated, id)
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	input, ok := readCredentials(w, r)
	if !ok {
		return
	}
	id, token, err := a.service.Login(r.Context(), input.Email, input.Password, clientIP(r), r.UserAgent())
	if err != nil {
		serviceError(w, r, err)
		return
	}
	a.setCookie(w, token, id.Session.ExpiresAt)
	writeJSON(w, http.StatusOK, id)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.service.Logout(r.Context(), a.token(r)); err != nil {
		serviceError(w, r, err)
		return
	}
	a.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, identity(r)) }

func (a *API) sessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := a.service.ListSessions(r.Context(), identity(r))
	if err != nil {
		serviceError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": sessions})
}

func (a *API) revoke(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	sessionID := chi.URLParam(r, "id")
	if err := a.service.Revoke(r.Context(), id.User.ID, sessionID); err != nil {
		serviceError(w, r, err)
		return
	}
	if sessionID == id.Session.ID {
		a.clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := a.service.LogoutAll(r.Context(), identity(r).User.ID); err != nil {
		serviceError(w, r, err)
		return
	}
	a.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) readiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.ready(ctx); err != nil {
		writeJSON(w, 503, map[string]string{"status": "not ready"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}
