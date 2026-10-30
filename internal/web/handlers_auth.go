package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"playpile/internal/auth"
)

// handleHome shows the landing page or the signed-in user's crews.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	_, user, ok := s.session(r)
	view := homeView{
		baseView:        s.base(r),
		SteamAPIKey:     s.cfg.SteamAPIKey != "",
		DefaultCountry:  s.cfg.DefaultCountry,
		DefaultLanguage: s.cfg.DefaultLanguage,
	}
	if ok {
		crews, err := s.st.CrewsForUser(user.ID)
		if err != nil {
			s.log.Error("list crews", "err", err)
			s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not load your crews.")
			return
		}
		view.Crews = crews
	}
	s.render(w, r, "home", http.StatusOK, view)
}

// handleLogin starts the Steam OpenID dance.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.session(r); ok {
		s.redirect(w, r, "/", "", "")
		return
	}
	nonce, err := randomHex(16)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not start the login.")
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	// The nonce ties the callback to this browser; `next` rides along so the
	// user lands back on the invite they clicked.
	s.setCookie(w, r, nonceCookie, nonce+"|"+next, 10*time.Minute)

	base := s.baseURL(r)
	target := base + "/auth/steam/callback"
	http.Redirect(w, r, s.oid.RedirectURL(base, target, nonce), http.StatusSeeOther)
}

// handleSteamCallback verifies Steam's answer and starts a session.
func (s *Server) handleSteamCallback(w http.ResponseWriter, r *http.Request) {
	nonce, next := s.readNonce(r)
	if nonce == "" || r.URL.Query().Get("n") != nonce {
		s.fail(w, r, http.StatusBadRequest, "Login failed",
			"This login did not start in this browser. Try signing in again.")
		return
	}
	s.clearCookie(w, r, nonceCookie)

	steamID, err := s.oid.Verify(r.Context(), r.URL.Query())
	if err != nil {
		if errors.Is(err, auth.ErrVerification) {
			s.fail(w, r, http.StatusBadRequest, "Login failed", "Steam did not confirm the sign-in.")
			return
		}
		s.log.Error("verify steam login", "err", err)
		s.fail(w, r, http.StatusBadGateway, "Steam is unreachable", "Could not reach Steam to verify the sign-in. Try again in a moment.")
		return
	}

	persona, avatar, profile := "", "", ""
	if s.steam != nil {
		if p, err := s.steam.PlayerSummary(r.Context(), steamID); err == nil {
			persona, avatar, profile = p.PersonaName, p.Avatar, p.ProfileURL
		} else {
			s.log.Warn("steam player summary", "err", err)
		}
	}

	user, err := s.st.UpsertSteamUser(steamID, persona, avatar, profile)
	if err != nil {
		s.log.Error("upsert user", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not save your account.")
		return
	}
	s.startSession(w, r, user.ID)
	s.redirect(w, r, next, "", "")
}

// handleDevLogin is the passwordless local login, only when explicitly enabled.
func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevLogin {
		s.notFound(w, r, "That page does not exist.")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "Bad request", "Could not read the form.")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Dev player"
	}
	steamID := r.FormValue("steamid")
	if steamID == "" {
		id, err := randomHex(6)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not create a dev login.")
			return
		}
		steamID = "dev" + id
	}
	user, err := s.st.UpsertSteamUser(steamID, name, "", "")
	if err != nil {
		s.log.Error("dev login", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not create the dev account.")
		return
	}
	s.startSession(w, r, user.ID)
	s.redirect(w, r, safeNext(r.FormValue("next")), "", "")
}

// handleLogout ends the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/", "", "")
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	_ = s.st.DeleteSession(sess.ID)
	s.clearCookie(w, r, sessionCookie)
	s.redirect(w, r, "/", "", "")
}

// handleProfile updates the display name.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	sess, user, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/login", "", "")
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	if err := s.st.SetDisplayName(user.ID, r.PostFormValue("name")); err != nil {
		s.back(w, r, "/", "A name is required.", "error")
		return
	}
	s.back(w, r, "/", "Name updated.", "ok")
}

// handleLibraryRefresh imports the player's Steam library so ownership is not
// typed in by hand. Needs an API key and a public profile.
func (s *Server) handleLibraryRefresh(w http.ResponseWriter, r *http.Request) {
	sess, user, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/login", "", "")
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	if s.steam == nil {
		s.back(w, r, "/", "Library import needs a Steam API key on the server.", "error")
		return
	}
	appids, err := s.steam.OwnedGames(r.Context(), user.SteamID)
	if err != nil {
		s.log.Warn("owned games", "err", err)
		s.back(w, r, "/", "Steam would not share your library. Set your profile's game details to public.", "error")
		return
	}
	if err := s.st.ReplaceLibrary(user.ID, appids); err != nil {
		s.log.Error("replace library", "err", err)
		s.back(w, r, "/", "Could not save the imported library.", "error")
		return
	}
	s.back(w, r, "/", "Imported "+itoa(len(appids))+" games from your Steam library.", "ok")
}

// startSession creates a session and sets its cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) {
	sess, err := s.st.CreateSession(userID, s.cfg.SessionTTL)
	if err != nil {
		s.log.Error("create session", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not start your session.")
		return
	}
	s.setCookie(w, r, sessionCookie, sess.ID, s.cfg.SessionTTL)
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secureCookies(r),
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secureCookies(r),
	})
}

func (s *Server) readNonce(r *http.Request) (nonce, next string) {
	c, err := r.Cookie(nonceCookie)
	if err != nil || c.Value == "" {
		return "", ""
	}
	nonce, next, _ = strings.Cut(c.Value, "|")
	return nonce, safeNext(next)
}

// safeNext only allows local redirect targets.
func safeNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func itoa(n int) string { return strconv.Itoa(n) }
