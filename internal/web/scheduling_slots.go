package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"playpile/internal/schedule"
	"playpile/internal/store"
)

// The view enumerates actual instants, never fabricating local wall-clock rows.
func scheduleTimetableDay(date, zone string, actor int64, days []store.AvailabilityDay) (scheduleDayVM, error) {
	loc, err := scheduleZone(zone)
	if err != nil {
		return scheduleDayVM{}, err
	}
	cells, err := schedule.DaySlots(date, zone)
	if err != nil {
		return scheduleDayVM{}, err
	}
	day := scheduleDayVM{Date: date, Label: cells[0].Start.In(loc).Format("Mon 02 Jan"), ShortLabel: cells[0].Start.In(loc).Format("Mon 2")}
	var own []store.AvailabilityDay
	for _, d := range days {
		if d.UserID == actor {
			own = append(own, d)
		}
	}
	canonical, err := store.CanonicalAvailabilityDays(own)
	if err != nil {
		return scheduleDayVM{}, err
	}
	var wants, blocked []schedule.Interval
	for _, d := range canonical {
		if d.UserID != actor {
			continue
		}
		if d.Date == date {
			wants = append(wants, d.Windows...)
			blocked = append(blocked, d.Blocked...)

		}
	}
	for _, cell := range cells {
		state, symbol := "unknown", "·"
		fullWant, anyWant, fullBlock, anyBlock := false, false, false, false
		for _, w := range wants {
			if !w.Start.After(cell.Start) && !w.End.Before(cell.End) {
				fullWant = true
			}
			if w.Start.Before(cell.End) && w.End.After(cell.Start) {
				anyWant = true
			}
		}
		for _, w := range blocked {
			if !w.Start.After(cell.Start) && !w.End.Before(cell.End) {
				fullBlock = true
			}
			if w.Start.Before(cell.End) && w.End.After(cell.Start) {
				anyBlock = true
			}
		}
		if fullBlock {
			state, symbol = "cant", "×"
		} else if fullWant && !anyBlock {
			state, symbol = "want", "✓"
		} else if anyWant || anyBlock {
			state, symbol = "mixed", "◐"
		}
		start, end := cell.Start.In(loc), cell.End.In(loc)
		label := start.Format("Mon 02 Jan 15:04 -07:00") + " → " + end.Format("15:04 -07:00")
		day.Slots = append(day.Slots, scheduleSlotVM{Start: cell.Start.UTC().Format(time.RFC3339), End: cell.End.UTC().Format(time.RFC3339), Time: start.Format("15:04"), Label: label, State: state, Symbol: symbol})
	}
	return day, nil
}

type scheduleSlotChange struct {
	Start string `json:"start"`
	State string `json:"state"`
}
type scheduleSlotEdit struct {
	Date    string               `json:"date"`
	Changes []scheduleSlotChange `json:"changes"`
}

func scheduleReadSlotEdits(r *http.Request) ([]scheduleSlotEdit, error) {
	if raw := r.PostForm.Get("edits"); raw != "" {
		var edits []scheduleSlotEdit
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&edits); err != nil {
			return nil, fmt.Errorf("invalid timetable changes")
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("invalid timetable changes")
		}
		return edits, nil
	}
	edit := scheduleSlotEdit{Date: r.PostForm.Get("date")}
	for key, values := range r.PostForm {
		if !strings.HasPrefix(key, "slot:") {
			continue
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("duplicate slot")
		}
		if values[0] != "" {
			edit.Changes = append(edit.Changes, scheduleSlotChange{Start: strings.TrimPrefix(key, "slot:"), State: values[0]})
		}
	}
	return []scheduleSlotEdit{edit}, nil
}

func (s *Server) handleScheduleSlots(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, user, ok := s.crewContext(w, r)
	if !ok || !s.schedulePostLimit(w, r, sess, 128<<10) {
		return
	}
	ajax := r.Header.Get("Accept") == "application/json"
	fail := func(err error) {
		if ajax {
			status := http.StatusBadRequest
			message := "Could not save. Your paint is still here; check the timezone and reload in another tab if needed."
			if errors.Is(err, errScheduleInternal) {
				status = http.StatusInternalServerError
				s.log.Error("save timetable", "err", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
			return
		}
		s.scheduleError(w, r, crew, mem, user, err)
	}
	loc, err := scheduleZone(r.PostForm.Get("tz"))
	if err != nil {
		fail(err)
		return
	}
	edits, err := scheduleReadSlotEdits(r)
	if err != nil {
		fail(err)
		return
	}
	if len(edits) == 0 || len(edits) > 7 {
		fail(fmt.Errorf("save between one and seven dates"))
		return
	}
	week, err := scheduleWeekBounds(time.Now(), loc)
	if err != nil {
		fail(err)
		return
	}
	days, err := s.st.AvailabilityDays(crew.ID, user.ID, week.Start, week.End)
	if err != nil {
		fail(scheduleStoreError(err))
		return
	}
	var own []store.AvailabilityDay
	for _, day := range days {
		if day.UserID == user.ID {
			own = append(own, day)
		}
	}
	days, err = store.CanonicalAvailabilityDays(own)
	if err != nil {
		fail(scheduleStoreError(err))
		return
	}
	batch := make([]store.AvailabilitySlotDay, 0, len(edits))
	seen := map[string]bool{}
	for _, edit := range edits {
		if seen[edit.Date] || len(edit.Changes) == 0 || len(edit.Changes) > 50 {
			fail(fmt.Errorf("invalid or duplicate edited date"))
			return
		}
		seen[edit.Date] = true
		if err = scheduleValidateDate(edit.Date, time.Now(), loc); err != nil {
			fail(err)
			return
		}
		cells, e := schedule.DaySlots(edit.Date, loc.String())
		if e != nil {
			fail(e)
			return
		}
		var wants, blocked []schedule.Interval
		for _, day := range days {
			if day.UserID != user.ID || day.Date != edit.Date {
				continue
			}
			if day.Zone != loc.String() {
				fail(store.ErrExists)
				return
			}
			wants, blocked = day.Windows, day.Blocked
		}
		touched := map[string]bool{}
		for _, change := range edit.Changes {
			if touched[change.Start] || (change.State != "want" && change.State != "cant" && change.State != "unknown") {
				fail(fmt.Errorf("invalid slot state"))
				return
			}
			touched[change.Start] = true
			found := false
			for _, cell := range cells {
				if cell.Start.UTC().Format(time.RFC3339) != change.Start {
					continue
				}
				paintState := map[string]string{"want": "want", "cant": "blocked", "unknown": "erase"}[change.State]
				wants, blocked = schedule.PaintIntervals(wants, blocked, cell, paintState)
				found = true
				break
			}
			if !found {
				fail(fmt.Errorf("choose an actual half-hour slot in this date"))
				return
			}
		}
		batch = append(batch, store.AvailabilitySlotDay{Date: edit.Date, Wants: wants, Blocked: blocked})
	}
	if err = s.st.SaveAvailabilitySlotsBatch(crew.ID, user.ID, loc.String(), batch); err != nil {
		fail(scheduleStoreError(err))
		return
	}
	if ajax {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"saved": true})
		return
	}
	s.scheduleSaved(w, r, crew.ID, "Your timetable was saved.")
}
