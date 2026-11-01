// Package web is playpile's HTTP layer: server-rendered pages plus a thin
// JSON surface for the small amount of JavaScript enhancement.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"playpile/internal/auth"
	"playpile/internal/catalog"
	"playpile/internal/money"
	"playpile/internal/store"
)

//go:embed all:views
var viewsFS embed.FS

//go:embed static
var staticFS embed.FS

// Config holds everything the HTTP layer needs to know about its environment.
type Config struct {
	// BaseURL is the public origin, e.g. https://games.example.com. When empty
	// it is derived from the request (fine behind a same-origin proxy).
	BaseURL string
	// DevLogin enables a passwordless local login for development.
	DevLogin bool
	// SteamAPIKey enables display names, avatars and library import.
	SteamAPIKey string
	// DefaultCountry is the fallback store region for new crews.
	DefaultCountry  string
	DefaultLanguage string
	SessionTTL      time.Duration
	// Version is shown in the footer.
	Version string
}

// Server wires the store, the Steam catalog and the templates into handlers.
type Server struct {
	cfg   Config
	st    *store.Store
	cat   *catalog.Catalog
	steam SteamAPI
	oid   *auth.OpenID
	log   *slog.Logger

	mux   *http.ServeMux
	pages map[string]*template.Template
	frag  *template.Template
}

// SteamAPI is the part of the Steam client that needs an API key.
type SteamAPI interface {
	PlayerSummary(ctx context.Context, steamID string) (Player, error)
	OwnedGames(ctx context.Context, steamID string) ([]int, error)
}

// Player mirrors the fields the web layer needs from a Steam profile.
type Player struct {
	SteamID     string
	PersonaName string
	Avatar      string
	ProfileURL  string
}

// New builds a server. steam may be nil when no API key is configured.
func New(cfg Config, st *store.Store, cat *catalog.Catalog, steam SteamAPI, log *slog.Logger) (*Server, error) {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.DefaultCountry == "" {
		cfg.DefaultCountry = "US"
	}
	if cfg.DefaultLanguage == "" {
		cfg.DefaultLanguage = "english"
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, st: st, cat: cat, steam: steam, oid: auth.NewOpenID(), log: log, mux: http.NewServeMux()}

	funcs := template.FuncMap{
		"money":    money.Format,
		"currency": func(cur string) string { return strings.ToUpper(cur) },
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return one
			}
			return many
		},
		"yesno": func(b bool, yes, no string) string {
			if b {
				return yes
			}
			return no
		},
	}

	pages := []string{"home", "crew", "game", "members", "join", "error", "manual", "schedule"}
	s.pages = make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		t, err := template.New("layout").Funcs(funcs).ParseFS(viewsFS, "views/layout.html", "views/_partials.html", "views/"+p+".html")
		if err != nil {
			return nil, fmt.Errorf("parse templates for %s: %w", p, err)
		}
		s.pages[p] = t
	}
	frag, err := template.New("frag").Funcs(funcs).ParseFS(viewsFS, "views/_partials.html")
	if err != nil {
		return nil, fmt.Errorf("parse partial templates: %w", err)
	}
	s.frag = frag

	s.routes()
	return s, nil
}

// Handler returns the root handler with middleware applied.
func (s *Server) Handler() http.Handler {
	return s.recoverer(s.logger(s.loadSession(s.mux)))
}

// Headers returns the static assets as a filesystem.
func staticFiles() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

func (s *Server) routes() {
	mux := s.mux

	mux.Handle("GET /static/", cacheStatic(http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles())))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("POST /profile", s.handleProfile)
	mux.HandleFunc("POST /library/refresh", s.handleLibraryRefresh)

	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("GET /auth/steam/callback", s.handleSteamCallback)
	mux.HandleFunc("GET /dev-login", s.handleDevLogin)
	mux.HandleFunc("POST /dev-login", s.handleDevLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	mux.HandleFunc("POST /crews", s.handleCreateCrew)

	mux.HandleFunc("GET /join/{token}", s.handleJoinPage)
	mux.HandleFunc("POST /join/{token}", s.handleJoin)

	mux.HandleFunc("GET /c/{crew}", s.handleCrew)
	mux.HandleFunc("GET /c/{crew}/search", s.handleSearch)
	mux.HandleFunc("GET /c/{crew}/members", s.handleMembers)
	mux.HandleFunc("GET /c/{crew}/schedule", s.handleSchedule)
	mux.HandleFunc("POST /c/{crew}/schedule/availability", s.handleScheduleAvailability)
	mux.HandleFunc("POST /c/{crew}/schedule/proposals", s.handleScheduleProposal)
	mux.HandleFunc("POST /c/{crew}/schedule/proposals/{id}/rsvp", s.handleScheduleRSVP)
	mux.HandleFunc("POST /c/{crew}/schedule/proposals/{id}/cancel", s.handleScheduleCancel)
	mux.HandleFunc("POST /c/{crew}/settings", s.handleCrewSettings)
	mux.HandleFunc("POST /c/{crew}/delete", s.handleDeleteCrew)
	mux.HandleFunc("POST /c/{crew}/refresh", s.handleRefreshPrices)
	mux.HandleFunc("POST /c/{crew}/invites", s.handleCreateInvite)
	mux.HandleFunc("POST /c/{crew}/invites/{token}/revoke", s.handleRevokeInvite)
	mux.HandleFunc("POST /c/{crew}/members/{id}/role", s.handleMemberRole)
	mux.HandleFunc("POST /c/{crew}/members/{id}/remove", s.handleRemoveMember)
	mux.HandleFunc("GET /c/{crew}/manual/new", s.handleManualForm)
	mux.HandleFunc("POST /c/{crew}/manual", s.handleManualForm)
	mux.HandleFunc("GET /c/{crew}/entries/{id}/edit", s.handleManualEdit)
	mux.HandleFunc("POST /c/{crew}/entries/{id}/edit", s.handleManualEdit)
	mux.HandleFunc("GET /c/{crew}/entries/{id}", s.handleGame)
	mux.HandleFunc("POST /c/{crew}/entries", s.handleAddEntry)
	mux.HandleFunc("POST /c/{crew}/entries/{id}/vote", s.handleVote)
	mux.HandleFunc("POST /c/{crew}/entries/{id}/own", s.handleOwn)
	mux.HandleFunc("POST /c/{crew}/entries/{id}/note", s.handleNote)
	mux.HandleFunc("POST /c/{crew}/entries/{id}/status", s.handleStatus)
	mux.HandleFunc("POST /c/{crew}/entries/{id}/delete", s.handleDeleteEntry)
	mux.HandleFunc("GET /c/{crew}/g/{appid}", s.handleGame)

	// Anything else gets the styled 404 instead of Go's plain text one.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.notFound(w, r, "That page does not exist.")
	})
}

// --- middleware -------------------------------------------------------------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic serving request", "path", r.URL.Path, "recover", rec)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/static/") {
			return
		}
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"dur", time.Since(start).Round(time.Millisecond).String())
	})
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

// --- requests, sessions, CSRF ----------------------------------------------

const (
	sessionCookie = "pp_session"
	nonceCookie   = "pp_login"
)

type ctxKey int

const (
	ctxSession ctxKey = iota
	ctxUser
)

// session returns the logged-in session and user, if any.
func (s *Server) session(r *http.Request) (store.Session, store.User, bool) {
	sess, ok := r.Context().Value(ctxSession).(store.Session)
	if !ok {
		return store.Session{}, store.User{}, false
	}
	user, ok := r.Context().Value(ctxUser).(store.User)
	if !ok {
		return store.Session{}, store.User{}, false
	}
	return sess, user, true
}

// loadSession resolves the session cookie for every request.
func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err == nil && c.Value != "" {
			if sess, err := s.st.SessionByID(c.Value); err == nil {
				if user, err := s.st.UserByID(sess.UserID); err == nil {
					ctx := context.WithValue(r.Context(), ctxSession, sess)
					ctx = context.WithValue(ctx, ctxUser, user)
					r = r.WithContext(ctx)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// baseURL returns the public origin for this request.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return strings.TrimRight(s.cfg.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

func (s *Server) secureCookies(r *http.Request) bool {
	return strings.HasPrefix(s.baseURL(r), "https://")
}

// wantsJSON reports whether the caller is the page's fetch helper.
func wantsJSON(r *http.Request) bool {
	if r.Header.Get("X-Playpile") == "fetch" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// isJSONBody reports whether this request carries a JSON body. Browsers cannot
// send one cross-site without a CORS preflight, so such requests are exempt
// from the CSRF token (which protects the plain form posts).
func isJSONBody(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}

// checkCSRF validates the token from a form body against the session.
func (s *Server) checkCSRF(r *http.Request, sess store.Session) bool {
	if isJSONBody(r) {
		return true
	}
	got := r.PostFormValue("csrf")
	return got != "" && subtleEqual(got, sess.CSRF)
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// --- responses -------------------------------------------------------------

// flash is a one-shot message passed through the query string of a redirect.
type flash struct {
	Message string
	Kind    string // "ok" or "error"
}

func flashOf(r *http.Request) flash {
	msg := strings.TrimSpace(r.URL.Query().Get("flash"))
	kind := r.URL.Query().Get("kind")
	if kind != "error" {
		kind = "ok"
	}
	return flash{Message: msg, Kind: kind}
}

// redirect sends the browser back with an optional flash message.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to, message, kind string) {
	if message != "" {
		u, err := url.Parse(to)
		if err == nil {
			q := u.Query()
			q.Set("flash", message)
			q.Set("kind", kind)
			u.RawQuery = q.Encode()
			to = u.String()
		}
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// back redirects to a local target, defaulting to home.
func (s *Server) back(w http.ResponseWriter, r *http.Request, to, message, kind string) {
	if to == "" || !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") {
		to = "/"
	}
	s.redirect(w, r, to, message, kind)
}

// jsonOK writes a successful JSON response for an enhanced form post.
func (s *Server) jsonOK(w http.ResponseWriter, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["ok"] = true
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}

// jsonError writes a JSON error, or falls back to a redirect with a flash.
func (s *Server) jsonError(w http.ResponseWriter, r *http.Request, status int, message, backTo string) {
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": message})
		return
	}
	s.back(w, r, backTo, message, "error")
}

// rejectCSRF answers a failed CSRF check.
func (s *Server) rejectCSRF(w http.ResponseWriter, r *http.Request) {
	if wantsJSON(r) {
		s.jsonError(w, r, http.StatusForbidden, "Your session expired. Reload the page.", "")
		return
	}
	s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload the page and try again.")
}

// render executes a page template.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, status int, data any) {
	t, ok := s.pages[page]
	if !ok {
		s.log.Error("unknown page template", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		s.log.Error("render page", "page", page, "err", err)
	}
}

// fragment renders one of the partial templates to a string, for JSON updates.
func (s *Server) fragment(name string, data any) template.HTML {
	var b strings.Builder
	if err := s.frag.ExecuteTemplate(&b, name, data); err != nil {
		s.log.Error("render fragment", "name", name, "err", err)
		return ""
	}
	return template.HTML(b.String())
}

// notFound renders the shared error page.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, message string) {
	s.fail(w, r, http.StatusNotFound, "Not found", message)
}

// fail renders an error page with a status code.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, title, message string) {
	s.render(w, r, "error", status, errorView{
		baseView: s.base(r),
		Code:     status,
		Title:    title,
		Message:  message,
	})
}

// idParam parses a numeric path segment.
func idParam(r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// intParam parses an int.
func intParam(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return n, true
}

var errUnauthorized = errors.New("unauthorized")
