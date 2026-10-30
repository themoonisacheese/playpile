package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"playpile/internal/money"
	"playpile/internal/pile"
	"playpile/internal/store"
)

// crewContext resolves the crew in the URL plus the caller's membership of it.
// It writes the response and returns ok=false when it cannot continue.
func (s *Server) crewContext(w http.ResponseWriter, r *http.Request) (store.Crew, store.Membership, store.Session, store.User, bool) {
	crewID, ok := idParam(r, "crew")
	if !ok {
		s.notFound(w, r, "That crew does not exist.")
		return store.Crew{}, store.Membership{}, store.Session{}, store.User{}, false
	}
	crew, err := s.st.CrewByID(crewID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r, "That crew does not exist.")
			return store.Crew{}, store.Membership{}, store.Session{}, store.User{}, false
		}
		s.log.Error("load crew", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not load that crew.")
		return store.Crew{}, store.Membership{}, store.Session{}, store.User{}, false
	}

	sess, user, ok := s.session(r)
	if !ok {
		s.redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), "", "")
		return crew, store.Membership{}, sess, user, false
	}

	mem, err := s.st.Membership(crew.ID, user.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, http.StatusForbidden, "Not your crew",
				"You are not a member of this crew. Ask someone there for an invite link.")
			return crew, store.Membership{}, sess, user, false
		}
		s.log.Error("load membership", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not load your membership.")
		return crew, store.Membership{}, sess, user, false
	}
	return crew, mem, sess, user, true
}

// viewFor builds the list page model for a crew.
func (s *Server) viewFor(r *http.Request, crew store.Crew, user store.User, opts pile.Options) (*crewPageView, error) {
	p, err := pile.Build(r.Context(), s.st, s.cat, crew, user, opts)
	if err != nil {
		return nil, err
	}
	filters, sorts := s.filterVMs(p)
	base := s.base(r)
	view := &crewPageView{
		baseView:    base,
		Crew:        crew,
		CrewID:      crew.ID,
		Role:        p.Me.Role,
		IsAdmin:     p.IsAdmin(),
		IsOwner:     p.IsOwner(),
		Pile:        p,
		Summary:     s.summaryVM(p),
		Entries:     s.entryVMs(p, p.Entries, base.CSRF),
		Filters:     filters,
		Sorts:       sorts,
		DataAgeText: dataAgeText(p.DataAge, time.Now()),
		MemberCount: len(p.Members),
		HasAPIKey:   s.cfg.SteamAPIKey != "",
	}
	view.CanImport = view.HasAPIKey && !strings.HasPrefix(user.SteamID, "dev")
	view.EmptyTitle, view.EmptyBody = emptyState(p)
	return view, nil
}

func emptyState(p *pile.Pile) (string, string) {
	switch {
	case p.Options.Filter == pile.FilterMissing:
		return "Nothing left to buy", "You already own everything on this list that has a price."
	case p.Options.Filter == pile.FilterReady:
		return "Nobody owns anything outright yet", "Games every member owns show up here."
	case p.Options.Filter == pile.FilterLiked:
		return "No up-votes yet", "Vote for the games you actually want to play."
	case p.Options.Filter == pile.FilterPlayed:
		return "Nothing played yet", "Games you finished or abandoned can be moved here."
	case len(p.All) == 0:
		return "An empty list", "Search above for a game and add the first one."
	}
	return "Nothing matches", "Try a different filter."
}

// handleCrew renders the list page.
func (s *Server) handleCrew(w http.ResponseWriter, r *http.Request) {
	crew, _, _, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	opts := pile.Options{Filter: r.URL.Query().Get("filter"), Sort: r.URL.Query().Get("sort")}
	view, err := s.viewFor(r, crew, user, opts)
	if err != nil {
		s.log.Error("build pile", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not build the list.")
		return
	}
	s.render(w, r, "crew", http.StatusOK, view)
}

// handleSearch is the add-game autocomplete.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	crew, _, _, _, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	term := strings.TrimSpace(r.URL.Query().Get("q"))
	type result struct {
		AppID int    `json:"appid"`
		Name  string `json:"name"`
		Image string `json:"image"`
		Price string `json:"price"`
		Meta  string `json:"meta"`
	}
	out := struct {
		Results []result `json:"results"`
	}{Results: []result{}}

	if len([]rune(term)) >= 2 {
		found, err := s.cat.Search(r.Context(), term, crew.Country, crew.Language)
		if err != nil {
			s.log.Warn("steam search", "term", term, "err", err)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "Steam search did not answer. Try again in a moment."})
			return
		}
		for _, item := range found {
			res := result{AppID: item.AppID, Name: item.Name, Image: item.TinyImage, Meta: item.Metascore}
			switch {
			case item.PriceFinal > 0:
				res.Price = money.Format(item.PriceFinal, item.Currency)
			case item.Currency != "":
				res.Price = "free"
			}
			out.Results = append(out.Results, res)
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

// handleCrewSettings renames a crew or changes its price region.
func (s *Server) handleCrewSettings(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, _, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	if !mem.Can(store.RoleAdmin) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Only admins can change the crew settings.")
		return
	}
	name := r.PostFormValue("name")
	country := r.PostFormValue("country")
	language := r.PostFormValue("language")
	if err := s.st.UpdateCrew(crew.ID, name, country, language); err != nil {
		s.back(w, r, fmt.Sprintf("/c/%d/members", crew.ID), "Could not save: "+err.Error(), "error")
		return
	}
	s.back(w, r, fmt.Sprintf("/c/%d/members", crew.ID), "Crew settings saved.", "ok")
}

// handleDeleteCrew removes a crew entirely.
func (s *Server) handleDeleteCrew(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, _, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	if mem.Role != store.RoleOwner {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Only the crew owner can delete it.")
		return
	}
	if err := s.st.DeleteCrew(crew.ID); err != nil {
		s.log.Error("delete crew", "err", err)
		s.back(w, r, fmt.Sprintf("/c/%d/members", crew.ID), "Could not delete the crew.", "error")
		return
	}
	s.redirect(w, r, "/", "Crew deleted.", "ok")
}

// handleRefreshPrices forces a Steam re-fetch for every game on the list.
func (s *Server) handleRefreshPrices(w http.ResponseWriter, r *http.Request) {
	crew, _, sess, _, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}

	entries, err := s.st.Entries(crew.ID, 0)
	if err != nil {
		s.log.Error("load entries for refresh", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not load the list.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	refreshed, steamEntries := 0, 0
	for _, e := range entries {
		if e.Manual != nil {
			continue
		}
		steamEntries++
		if err := s.cat.RefreshGame(ctx, e.AppID, crew.Country, crew.Language); err != nil {
			s.log.Warn("refresh game", "appid", e.AppID, "err", err)
			continue
		}
		_ = s.cat.RefreshReviews(ctx, e.AppID)
		refreshed++
	}
	if refreshed < steamEntries {
		s.back(w, r, fmt.Sprintf("/c/%d", crew.ID),
			fmt.Sprintf("Refreshed %d of %d games from Steam.", refreshed, steamEntries), "error")
		return
	}
	s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Refreshed every game from Steam.", "ok")
}

// handleCreateInvite mints an invite link.
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	if !mem.Can(store.RoleAdmin) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Only admins can create invites.")
		return
	}

	role := r.PostFormValue("role")
	if role != store.RoleAdmin {
		role = store.RoleMember
	}
	var expiresAt int64
	if days, ok := intParam(r.PostFormValue("days")); ok && days > 0 {
		if days > 365 {
			days = 365
		}
		expiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour).Unix()
	}
	var maxUses int
	if uses, ok := intParam(r.PostFormValue("uses")); ok && uses > 0 {
		maxUses = uses
	}

	inv, err := s.st.CreateInvite(crew.ID, user.ID, role, expiresAt, maxUses)
	if err != nil {
		s.log.Error("create invite", "err", err)
		s.back(w, r, fmt.Sprintf("/c/%d/members", crew.ID), "Could not create the invite.", "error")
		return
	}
	s.redirect(w, r, fmt.Sprintf("/c/%d/members?new=%s", crew.ID, url.QueryEscape(inv.Token)), "Invite link created.", "ok")
}

// handleRevokeInvite kills an invite link.
func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, _, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	if !mem.Can(store.RoleAdmin) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Only admins can revoke invites.")
		return
	}
	token := r.PathValue("token")
	if err := s.st.RevokeInvite(crew.ID, token); err != nil {
		s.back(w, r, fmt.Sprintf("/c/%d/members", crew.ID), "Could not revoke that invite.", "error")
		return
	}
	s.back(w, r, fmt.Sprintf("/c/%d/members", crew.ID), "Invite revoked.", "ok")
}

// unused keeps strconv in the import list when a build tag removes helpers.
var _ = strconv.Itoa

// handleCreateCrew makes a new crew with the caller as owner.
func (s *Server) handleCreateCrew(w http.ResponseWriter, r *http.Request) {
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
	name := r.PostFormValue("name")
	country := r.PostFormValue("country")
	if strings.TrimSpace(country) == "" {
		country = s.cfg.DefaultCountry
	}
	crew, err := s.st.CreateCrew(name, country, s.cfg.DefaultLanguage, user.ID)
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			s.back(w, r, "/", "A crew needs a name.", "error")
			return
		}
		s.log.Error("create crew", "err", err)
		s.back(w, r, "/", "Could not create the crew.", "error")
		return
	}
	s.redirect(w, r, fmt.Sprintf("/c/%d", crew.ID), "Crew created. Send somebody an invite link.", "ok")
}
