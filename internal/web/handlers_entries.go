package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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

// addAppID reads the app id from either a JSON body or a form post.
func addAppID(w http.ResponseWriter, r *http.Request) (int, bool) {
	if isJSONBody(r) {
		var body struct {
			AppID int `json:"appid"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			return 0, false
		}
		return body.AppID, body.AppID > 0
	}
	if err := r.ParseForm(); err != nil {
		return 0, false
	}
	appid, ok := intParam(r.PostFormValue("appid"))
	return appid, ok && appid > 0
}

// handleAddEntry puts a game on the list.
func (s *Server) handleAddEntry(w http.ResponseWriter, r *http.Request) {
	crew, _, sess, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r, sess) {
		s.rejectCSRF(w, r)
		return
	}
	appid, valid := addAppID(w, r)
	if !valid {
		s.jsonError(w, r, http.StatusBadRequest, "That game id did not look right.", fmt.Sprintf("/c/%d", crew.ID))
		return
	}

	// Fetch metadata first: adding a game Steam will not sell us in this region
	// is almost always a typo or a region-locked stub.
	if _, err := s.cat.Game(r.Context(), appid, crew.Country, crew.Language); err != nil {
		s.log.Warn("add entry metadata", "appid", appid, "err", err)
		s.jsonError(w, r, http.StatusBadGateway, "Steam has no store page for that game in this region.", fmt.Sprintf("/c/%d", crew.ID))
		return
	}

	if _, err := s.st.AddEntry(crew.ID, appid, user.ID); err != nil {
		if errors.Is(err, store.ErrExists) {
			s.jsonError(w, r, http.StatusConflict, "That game is already on the list.", fmt.Sprintf("/c/%d", crew.ID))
			return
		}
		s.log.Error("add entry", "err", err)
		s.jsonError(w, r, http.StatusInternalServerError, "Could not add that game.", fmt.Sprintf("/c/%d", crew.ID))
		return
	}

	if wantsJSON(r) {
		s.jsonOK(w, map[string]any{"reload": true})
		return
	}
	s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Added to the list.", "ok")
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
	if err := s.st.SetOwned(user.ID, entry.AppID, owned, store.SourceManual); err != nil {
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
