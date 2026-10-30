package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"playpile/internal/catalog"
	"playpile/internal/pile"
	"playpile/internal/store"
)

// entryVMByID re-renders one entry from the freshly built pile.
func (s *Server) entryVMByID(view *crewPageView, id int64) (entryVM, bool) {
	for _, e := range view.Pile.All {
		if e.ID == id {
			return s.entryVM(view.Pile, e, view.CSRF), true
		}
	}
	return entryVM{}, false
}

// addRequest is what an add-game post asked for: a Steam app id, or a term
// typed into the box that still has to be resolved (a name, a store link, or
// an id someone typed rather than clicked).
type addRequest struct {
	AppID int
	Term  string
}

// readAddRequest reads the app id and/or search term from a JSON body or a
// plain form post. malformed reports a body that could not be parsed.
func readAddRequest(w http.ResponseWriter, r *http.Request) (req addRequest, malformed bool) {
	if isJSONBody(r) {
		var body struct {
			AppID int    `json:"appid"`
			Q     string `json:"q"`
			Term  string `json:"term"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			return addRequest{}, true
		}
		req = addRequest{AppID: body.AppID, Term: firstNonEmpty(body.Q, body.Term)}
		return req, false
	}
	if err := r.ParseForm(); err != nil {
		return addRequest{}, true
	}
	appid, _ := intParam(r.PostFormValue("appid"))
	req = addRequest{
		AppID: appid,
		Term:  firstNonEmpty(r.PostFormValue("q"), r.PostFormValue("term")),
	}
	return req, false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// storeAppLink matches the app id in a Steam store URL, so a pasted
// "https://store.steampowered.com/app/620/Portal_2/" works too.
var storeAppLink = regexp.MustCompile(`/app/([0-9]+)`)

// appIDFromText pulls an app id out of a bare number or a Steam store link.
func appIDFromText(text string) (int, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	if id, err := strconv.Atoi(text); err == nil && id > 0 {
		return id, true
	}
	if m := storeAppLink.FindStringSubmatch(text); m != nil {
		if id, err := strconv.Atoi(m[1]); err == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}

// resolveAdd turns a request into an app id. A name is looked up on the store:
// an exact name wins, a single match is taken, and anything ambiguous is
// refused with the candidates so the picker can be used instead. On failure it
// returns the HTTP status and the sentence to show.
func (s *Server) resolveAdd(ctx context.Context, crew store.Crew, req addRequest) (int, int, string) {
	if req.AppID > 0 {
		return req.AppID, 0, ""
	}
	if id, ok := appIDFromText(req.Term); ok {
		return id, 0, ""
	}
	if req.Term == "" {
		return 0, http.StatusBadRequest, "Type a game name, a Steam app id, or a store link."
	}

	results, err := s.cat.Search(ctx, req.Term, crew.Country, crew.Language)
	if err != nil {
		s.log.Warn("add entry search", "term", req.Term, "err", err)
		return 0, http.StatusBadGateway, "Steam search did not answer. Try again in a moment."
	}
	if len(results) == 0 {
		return 0, http.StatusNotFound, fmt.Sprintf("Steam has no game called %q.", req.Term)
	}
	want := strings.ToLower(req.Term)
	for _, r := range results {
		if strings.ToLower(strings.TrimSpace(r.Name)) == want {
			return r.AppID, 0, ""
		}
	}
	if len(results) == 1 {
		return results[0].AppID, 0, ""
	}
	names := make([]string, 0, 3)
	for _, r := range results {
		if len(names) == 3 {
			break
		}
		names = append(names, r.Name)
	}
	return 0, http.StatusConflict, fmt.Sprintf("Several games match %q — pick one: %s.", req.Term, strings.Join(names, ", "))
}

// handleAddEntry puts a game on the list, by app id or by name.
func (s *Server) handleAddEntry(w http.ResponseWriter, r *http.Request) {
	crew, _, sess, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r, sess) {
		s.rejectCSRF(w, r)
		return
	}
	back := fmt.Sprintf("/c/%d", crew.ID)

	req, malformed := readAddRequest(w, r)
	if malformed {
		s.jsonError(w, r, http.StatusBadRequest, "That game id did not look right.", back)
		return
	}
	appid, status, message := s.resolveAdd(r.Context(), crew, req)
	if status != 0 {
		s.jsonError(w, r, status, message, back)
		return
	}

	// Fetch metadata first: a game Steam will not sell us in this region is
	// almost always a typo or a region-locked stub, and adding it anyway would
	// leave a nameless row on the list.
	if _, err := s.cat.Game(r.Context(), appid, crew.Country, crew.Language); err != nil {
		s.log.Warn("add entry metadata", "appid", appid, "err", err)
		if errors.Is(err, catalog.ErrUnavailable) {
			s.jsonError(w, r, http.StatusNotFound, "Steam has no store page for that game in this region.", back)
			return
		}
		s.jsonError(w, r, http.StatusBadGateway, "Steam did not answer. Try again in a moment.", back)
		return
	}

	if _, err := s.st.AddEntry(crew.ID, appid, user.ID); err != nil {
		if errors.Is(err, store.ErrExists) {
			s.jsonError(w, r, http.StatusConflict, "That game is already on the list.", back)
			return
		}
		s.log.Error("add entry", "err", err)
		s.jsonError(w, r, http.StatusInternalServerError, "Could not add that game.", back)
		return
	}

	if wantsJSON(r) {
		s.jsonOK(w, map[string]any{"reload": true})
		return
	}
	s.back(w, r, back, "Added to the list.", "ok")
}

// handleVote records an up/down vote and returns the fresh summary strip.
func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	crew, _, sess, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.rejectCSRF(w, r)
		return
	}
	entryID, ok := idParam(r, "id")
	if !ok {
		s.jsonError(w, r, http.StatusBadRequest, "Unknown entry.", "")
		return
	}
	value, valid := intParam(r.PostFormValue("value"))
	if !valid || value < -1 || value > 1 {
		s.jsonError(w, r, http.StatusBadRequest, "Votes are 1, -1 or 0.", "")
		return
	}

	if err := s.st.Vote(crew.ID, entryID, user.ID, value); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.jsonError(w, r, http.StatusNotFound, "That game is no longer on the list.", fmt.Sprintf("/c/%d", crew.ID))
			return
		}
		s.log.Error("vote", "err", err)
		s.jsonError(w, r, http.StatusInternalServerError, "Could not save the vote.", "")
		return
	}

	view, err := s.viewFor(r, crew, user, pile.Options{Filter: r.PostFormValue("filter"), Sort: r.PostFormValue("sort")})
	if err != nil {
		s.log.Error("rebuild after vote", "err", err)
		s.jsonOK(w, map[string]any{"reload": true})
		return
	}
	entry, found := s.entryVMByID(view, entryID)
	if !found {
		s.jsonOK(w, map[string]any{"reload": true})
		return
	}
	s.jsonOK(w, map[string]any{
		"up":           entry.Up,
		"down":         entry.Down,
		"score":        entry.Score,
		"mine":         entry.MyVote,
		"vetoed":       entry.Vetoed,
		"summary_html": s.fragment("summary", view),
	})
}

// handleOwn toggles "I own this" for the viewer.
func (s *Server) handleOwn(w http.ResponseWriter, r *http.Request) {
	crew, _, sess, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.rejectCSRF(w, r)
		return
	}
	entryID, ok := idParam(r, "id")
	if !ok {
		s.jsonError(w, r, http.StatusBadRequest, "Unknown entry.", "")
		return
	}
	entry, err := s.st.EntryByID(crew.ID, entryID)
	if err != nil {
		s.jsonError(w, r, http.StatusNotFound, "That game is no longer on the list.", fmt.Sprintf("/c/%d", crew.ID))
		return
	}
	owned := r.PostFormValue("owned") == "1"
	if entry.Manual != nil {
		err = s.st.SetManualOwned(crew.ID, entry.ID, user.ID, owned)
	} else {
		err = s.st.SetOwned(user.ID, entry.AppID, owned, store.SourceManual)
	}
	if err != nil {
		s.log.Error("set owned", "err", err)
		s.jsonError(w, r, http.StatusInternalServerError, "Could not save that.", "")
		return
	}

	view, err := s.viewFor(r, crew, user, pile.Options{Filter: r.PostFormValue("filter"), Sort: r.PostFormValue("sort")})
	if err != nil {
		s.log.Error("rebuild after ownership", "err", err)
		s.jsonOK(w, map[string]any{"reload": true})
		return
	}
	row, found := s.entryVMByID(view, entryID)
	if !found {
		s.jsonOK(w, map[string]any{"reload": true})
		return
	}
	s.jsonOK(w, map[string]any{
		"owners_html":  s.fragment("owners", row),
		"mine_html":    s.fragment("mine", row),
		"summary_html": s.fragment("summary", view),
	})
}

// handleNote saves the crew's note on a game.
func (s *Server) handleNote(w http.ResponseWriter, r *http.Request) {
	crew, _, sess, _, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	entryID, ok := idParam(r, "id")
	if !ok {
		s.notFound(w, r, "Unknown entry.")
		return
	}
	if err := s.st.SetEntryNote(crew.ID, entryID, r.PostFormValue("note")); err != nil {
		s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Could not save the note.", "error")
		return
	}
	s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Note saved.", "ok")
}

// handleStatus moves a game to (or back from) "played".
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	crew, _, sess, _, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if !s.checkCSRF(r, sess) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return
	}
	entryID, ok := idParam(r, "id")
	if !ok {
		s.notFound(w, r, "Unknown entry.")
		return
	}
	status := r.PostFormValue("status")
	if err := s.st.SetEntryStatus(crew.ID, entryID, status); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Unknown status.", "error")
			return
		}
		s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Could not move that game.", "error")
		return
	}
	s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "List updated.", "ok")
}

// handleDeleteEntry removes a game from the list (admins only).
func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
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
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Only admins can remove games from the list.")
		return
	}
	entryID, ok := idParam(r, "id")
	if !ok {
		s.notFound(w, r, "Unknown entry.")
		return
	}
	if err := s.st.DeleteEntry(crew.ID, entryID); err != nil {
		s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Could not remove that game.", "error")
		return
	}
	s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Game removed.", "ok")
}
