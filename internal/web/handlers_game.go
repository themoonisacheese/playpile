package web

import (
	"fmt"
	"net/http"
	"strings"

	"playpile/internal/pile"
	"playpile/internal/store"
)

// handleGame shows one game's detail page inside a crew.
func (s *Server) handleGame(w http.ResponseWriter, r *http.Request) {
	crew, _, _, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	appid, ok := intParam(r.PathValue("appid"))
	if !ok || appid <= 0 {
		s.notFound(w, r, "Unknown game.")
		return
	}
	view, err := s.viewFor(r, crew, user, pile.Options{})
	if err != nil {
		s.log.Error("build pile for game page", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Something broke", "Could not load that game.")
		return
	}

	var found *pile.Entry
	for i := range view.Pile.All {
		if view.Pile.All[i].AppID == appid {
			found = &view.Pile.All[i]
			break
		}
	}
	if found == nil {
		s.notFound(w, r, "That game is not on this crew's list.")
		return
	}

	row := s.entryVM(view.Pile, *found, view.CSRF)
	facts := GameFacts{
		Name:       row.Name,
		Cover:      found.Game.HeaderImage,
		Short:      found.Game.ShortDescription,
		Release:    found.Game.ReleaseDate,
		IsFree:     row.IsFree,
		PriceLabel: row.PriceLabel,
		MetaScore:  row.MetaScore,
		MetaURL:    row.MetaURL,
		ReviewDesc: row.ReviewDesc,
		ReviewPct:  row.ReviewPct,
		Devs:       strings.Join(found.Game.Developers, ", "),
		Pubs:       strings.Join(found.Game.Publishers, ", "),
		Genres:     strings.Join(found.Game.Genres, ", "),
		Platforms:  titleCase(strings.Join(found.Game.Platforms, ", ")),
	}
	if found.Reviews != nil {
		facts.ReviewTotal = found.Reviews.TotalReviews
	}

	s.render(w, r, "game", http.StatusOK, &gamePageView{
		baseView: s.base(r),
		Crew:     crew,
		CrewID:   crew.ID,
		Entry:    row,
		Game:     facts,
		BackHref: fmt.Sprintf("/c/%d", crew.ID),
	})
}

// unused placeholder keeps the import meaningful if the page shrinks later.
var _ = store.StatusActive

func titleCase(s string) string {
	parts := strings.Split(s, ", ")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, ", ")
}
