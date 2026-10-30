package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"playpile/internal/money"
	"playpile/internal/store"
)

type manualFields struct{ Name, Description, ImageURL, StoreURL, Price, MetaScore string }
type manualFormView struct {
	baseView
	Crew                             store.Crew
	CrewID                           int64
	EntryID                          int64
	Fields                           manualFields
	Action, Heading, Error, Currency string
}

func (s *Server) handleManualForm(w http.ResponseWriter, r *http.Request) { s.manualForm(w, r, false) }
func (s *Server) handleManualEdit(w http.ResponseWriter, r *http.Request) { s.manualForm(w, r, true) }

func (s *Server) manualForm(w http.ResponseWriter, r *http.Request, editing bool) {
	crew, member, sess, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	view := &manualFormView{baseView: s.base(r), Crew: crew, CrewID: crew.ID, Currency: money.CurrencyForCountry(crew.Country), Heading: "add a non-Steam game", Action: fmt.Sprintf("/c/%d/manual", crew.ID)}
	if editing {
		id, valid := idParam(r, "id")
		if !valid {
			s.notFound(w, r, "Unknown game.")
			return
		}
		entry, err := s.st.EntryByID(crew.ID, id)
		if err != nil || entry.Manual == nil {
			s.notFound(w, r, "Unknown manual game.")
			return
		}
		if !member.Can(store.RoleAdmin) && entry.AddedBy != user.ID {
			s.fail(w, r, http.StatusForbidden, "Forbidden", "Only the person who added this game or an admin can edit it.")
			return
		}
		view.EntryID = id
		view.Heading = "edit game details"
		view.Action = fmt.Sprintf("/c/%d/entries/%d/edit", crew.ID, id)
		m := entry.Manual
		view.Fields = manualFields{Name: m.Name, Description: m.Description, ImageURL: m.ImageURL, StoreURL: m.StoreURL}
		if m.PriceMinor != nil && m.Currency == view.Currency {
			view.Fields.Price = money.Decimal(*m.PriceMinor, m.Currency)
		}
		if m.PriceMinor != nil && m.Currency != view.Currency {
			view.Error = "The crew currency has changed. Enter a new price in " + view.Currency + "; the old amount has not been converted."
		}
		if m.MetaScore != nil {
			view.Fields.MetaScore = strconv.Itoa(*m.MetaScore)
		}
	}
	if r.Method == http.MethodGet {
		s.render(w, r, "manual", http.StatusOK, view)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "Invalid form", "The form was too large or malformed.")
		return
	}
	if !s.checkCSRF(r, sess) {
		s.rejectCSRF(w, r)
		return
	}
	view.Fields = manualFields{
		Name: strings.TrimSpace(r.PostFormValue("name")), Description: strings.TrimSpace(r.PostFormValue("description")),
		ImageURL: strings.TrimSpace(r.PostFormValue("image_url")), StoreURL: strings.TrimSpace(r.PostFormValue("store_url")),
		Price: strings.TrimSpace(r.PostFormValue("price")), MetaScore: strings.TrimSpace(r.PostFormValue("metascore")),
	}
	invalid := func(message string) { view.Error = message; s.render(w, r, "manual", http.StatusBadRequest, view) }
	price, err := money.Parse(view.Fields.Price, view.Currency)
	if err != nil {
		invalid("Enter a valid nonnegative price in " + view.Currency + " (or leave it blank if unknown).")
		return
	}
	var meta *int
	if view.Fields.MetaScore != "" {
		score, err := strconv.Atoi(view.Fields.MetaScore)
		if err != nil || score < 0 || score > 100 {
			invalid("Metascore must be between 0 and 100, or blank if unknown.")
			return
		}
		meta = &score
	}
	game := store.ManualGame{Name: view.Fields.Name, Description: view.Fields.Description, ImageURL: view.Fields.ImageURL, StoreURL: view.Fields.StoreURL, PriceMinor: price, Currency: view.Currency, MetaScore: meta}
	if editing {
		err = s.st.UpdateManualEntry(crew.ID, view.EntryID, game)
	} else {
		_, err = s.st.AddManualEntry(crew.ID, game, user.ID)
	}
	if errors.Is(err, store.ErrInvalid) {
		invalid("Check the game name (1–200 characters), description (up to 2000 characters), and links (http or https only).")
		return
	}
	if err != nil {
		s.log.Error("save manual game", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Could not save", "Could not save this game. Your changes have not been applied.")
		return
	}
	s.back(w, r, fmt.Sprintf("/c/%d", crew.ID), "Saved the game.", "ok")
}
