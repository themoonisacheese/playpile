package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"playpile/internal/schedule"
	"playpile/internal/store"
)

type scheduleInputError struct{ error }
type scheduleZoneConflict struct{ Zone string }

func (e scheduleZoneConflict) Error() string {
	return fmt.Sprintf("Your saved availability uses %s; clear each future dated answer below before switching timezone.", e.Zone)
}

var errScheduleInternal = errors.New("internal scheduling failure")

func scheduleStoreError(err error) error {
	if err == nil || errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrExists) {
		return err
	}
	return fmt.Errorf("%w: %v", errScheduleInternal, err)
}
func scheduleURL(crewID int64, zone, duration string) string {
	return fmt.Sprintf("/c/%d/schedule?", crewID) + url.Values{"tz": {zone}, "duration": {duration}}.Encode()
}

// scheduleDateRange is shared by rendering and save validation so hidden future
// answers cannot lock a timezone beyond the dates the editor can show.
func scheduleDateRange(now time.Time, loc *time.Location) (string, string) {
	local := now.In(loc)
	return local.Format("2006-01-02"), local.AddDate(0, 0, 6).Format("2006-01-02")
}

// scheduleWeekBounds ends at the last civil day's actual boundary, even when
// today's midnight was skipped and the first instant is 01:00.
func scheduleWeekBounds(now time.Time, loc *time.Location) (schedule.Interval, error) {
	firstDate, lastDate := scheduleDateRange(now, loc)
	first, err := schedule.DayBounds(firstDate, loc.String())
	if err != nil {
		return schedule.Interval{}, err
	}
	last, err := schedule.DayBounds(lastDate, loc.String())
	if err != nil {
		return schedule.Interval{}, err
	}
	return schedule.Interval{Start: first.Start, End: last.End}, nil
}

func scheduleValidateDate(date string, now time.Time, loc *time.Location) error {
	first, last := scheduleDateRange(now, loc)
	if _, err := time.Parse("2006-01-02", date); err != nil || date < first || date > last {
		return fmt.Errorf("choose a date in the 7-day planning view (%s through %s); use clear another saved date only to remove an older answer", first, last)
	}
	return nil
}
func scheduleDuration(raw string) string {
	if raw == "1" || raw == "3" {
		return raw
	}
	return "2"
}
func scheduleZone(raw string) (*time.Location, error) {
	if raw == "" {
		raw = "UTC"
	}
	if raw == "Local" || len(raw) > 100 {
		return nil, fmt.Errorf("choose an IANA timezone such as Europe/Paris or UTC")
	}
	loc, err := time.LoadLocation(raw)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone: use an IANA name such as Europe/Paris or UTC")
	}
	return loc, nil
}
func scheduleTimeLabel(start, end time.Time, loc *time.Location) string {
	return start.In(loc).Format("Mon 02 Jan, 15:04 MST") + " → " + end.In(loc).Format("Mon 02 Jan, 15:04 MST")
}
func (s *Server) scheduleView(r *http.Request, crew store.Crew, mem store.Membership, user store.User, zone, duration string, fields *scheduleFields) (*schedulePageView, error) {
	planning, err := s.st.PlanningMembers(crew.ID, user.ID)
	if err != nil {
		return nil, err
	}
	if zone == "" {
		for _, m := range planning {
			if m.UserID == user.ID {
				zone = m.Zone
			}
		}
	}
	loc, err := scheduleZone(zone)
	if err != nil {
		return nil, scheduleInputError{err}
	}
	for _, m := range planning {
		if m.UserID != user.ID || m.Zone == "" || m.Zone == loc.String() {
			continue
		}
		rangeStart := time.Now()
		future, e := s.st.AvailabilityDays(crew.ID, user.ID, rangeStart, rangeStart.Add(28*24*time.Hour))
		if e != nil {
			return nil, e
		}
		for _, d := range future {
			if d.UserID == user.ID {
				return nil, scheduleZoneConflict{Zone: m.Zone}
			}
		}
	}
	zone = loc.String()
	now := time.Now()
	first, err := scheduleWeekBounds(now, loc)
	if err != nil {
		return nil, err
	}
	from := first.Start
	to := first.End
	days, err := s.st.AvailabilityDays(crew.ID, user.ID, from, to)
	if err != nil {
		return nil, err
	}
	members, err := s.st.Members(crew.ID)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, m := range members {
		names[m.ID] = m.DisplayName
	}
	avail := make([]schedule.MemberAvailability, 0, len(planning))
	for _, m := range planning {
		a := schedule.MemberAvailability{UserID: m.UserID}
		for _, d := range days {
			if d.UserID != m.UserID {
				continue
			}
			bounds, e := schedule.DayBounds(d.Date, d.Zone)
			if e != nil {
				return nil, e
			}
			if !d.Explicit {
				a.Reviewed = append(a.Reviewed, bounds)
			}
			a.Blocked = append(a.Blocked, d.Blocked...)
			a.Windows = append(a.Windows, d.Windows...)
		}
		avail = append(avail, a)
	}
	view := &schedulePageView{baseView: s.base(r), Crew: crew, CrewID: crew.ID, Zone: zone, Duration: scheduleDuration(duration)}
	selected := r.URL.Query().Get("date")
	if selected == "" {
		selected = from.In(loc).Format("2006-01-02")
	}
	if err := scheduleValidateDate(selected, now, loc); err != nil {
		return nil, scheduleInputError{err}
	}
	view.MinDate, view.MaxDate = scheduleDateRange(now, loc)
	view.Fields = scheduleFields{Date: selected, Status: "", ProposalStart: r.URL.Query().Get("proposal_start"), ProposalEnd: r.URL.Query().Get("proposal_end"), ProposalStartInstant: r.URL.Query().Get("proposal_start_instant"), ProposalEndInstant: r.URL.Query().Get("proposal_end_instant")}
	for i := 0; i < 7; i++ {
		date := from.In(loc).AddDate(0, 0, i)
		key := date.Format("2006-01-02")
		state := "Unknown — not answered"
		for _, d := range days {
			if d.UserID != user.ID || d.Date != key {
				continue
			}
			state = "Checked — unavailable"
			if d.Explicit {
				state = "Marked slots · blank time not answered"
			}
			if len(d.Windows) > 0 {
				var labels []string
				for _, w := range d.Windows {
					label := w.Start.In(loc).Format("15:04") + "–" + w.End.In(loc).Format("15:04")
					if w.End.In(loc).Format("2006-01-02") != key {
						label += " (+1 day)"
					}
					labels = append(labels, label)
				}
				state = "Want to play: " + strings.Join(labels, ", ")
			}
			if d.Explicit {
				if len(d.Blocked) > 0 {
					state += fmt.Sprintf(" · %d can't-play window(s)", len(d.Blocked))
				}
				if len(d.Windows) > 0 {
					state += " · blank time not answered"
				}
			}
			if selected == key {
				view.Fields.Status = "unavailable"
				if len(d.Windows) > 0 {
					view.Fields.Status = "available"
				}
				for _, w := range d.Windows {
					view.Fields.Windows = append(view.Fields.Windows, scheduleWindowFields{Start: w.Start.In(loc).Format("15:04"), End: w.End.In(loc).Format("15:04"), Overnight: w.End.In(loc).Format("2006-01-02") != key})
				}
			}
		}
		day, e := scheduleTimetableDay(key, zone, user.ID, days)
		if e != nil {
			return nil, e
		}
		day.State, day.EditURL = state, scheduleURL(crew.ID, zone, view.Duration)+"&date="+key
		for j := range day.Slots {
			day.Slots[j].Draft = r.PostForm.Get("slot:" + day.Slots[j].Start)
		}
		view.Days = append(view.Days, day)
	}
	view.CrewMembers, err = scheduleCrewTimetable(view.Days, members, user.ID, days, zone)
	if err != nil {
		return nil, err
	}
	if fields != nil {
		view.Fields = *fields
	}
	if start, e := scheduleHintedTime(view.Fields.ProposalStart, zone, view.Fields.ProposalStartInstant); e == nil && view.Fields.ProposalStartInstant != "" {
		if end, e := scheduleHintedTime(view.Fields.ProposalEnd, zone, view.Fields.ProposalEndInstant); e == nil && view.Fields.ProposalEndInstant != "" {
			view.ProposalOffsetNote = "Selected window: " + start.In(loc).Format("02 Jan 15:04 -07:00") + " → " + end.In(loc).Format("02 Jan 15:04 -07:00") + ". Exact instants are retained unless you edit the local times."
		}
	}
	for i, w := range view.Fields.Windows {
		if i > 0 && (w.Start != "" || w.End != "") {
			view.MoreWindows = true
		}
	}
	for len(view.Fields.Windows) < 8 {
		view.Fields.Windows = append(view.Fields.Windows, scheduleWindowFields{})
	}
	idsLabel := func(ids []int64) string {
		var out []string
		for _, id := range ids {
			out = append(out, names[id])
		}
		if len(out) == 0 {
			return "none"
		}
		return strings.Join(out, ", ")
	}
	hours, _ := strconv.Atoi(view.Duration)
	for _, candidate := range schedule.Suggestions(avail, now.Truncate(time.Minute).Add(time.Minute), to, time.Duration(hours)*time.Hour, 24) {
		proposalEnd := candidate.End
		if proposalEnd.Sub(candidate.Start) > 12*time.Hour {
			proposalEnd = candidate.Start.Add(12 * time.Hour)
		}
		u := scheduleURL(crew.ID, zone, view.Duration) + "&" + url.Values{"proposal_start": {candidate.Start.In(loc).Format("2006-01-02T15:04")}, "proposal_end": {proposalEnd.In(loc).Format("2006-01-02T15:04")}, "proposal_start_instant": {candidate.Start.UTC().Format(time.RFC3339)}, "proposal_end_instant": {proposalEnd.UTC().Format(time.RFC3339)}}.Encode() + "#propose"
		c := scheduleCandidateVM{Label: scheduleTimeLabel(candidate.Start, candidate.End, loc), Href: u, Whole: candidate.FullCrew, Detail: "Want to play: " + idsLabel(candidate.Participants) + ". Needs an answer for part/all of this window: " + idsLabel(candidate.Unknown) + ". Cannot make the whole window: " + idsLabel(candidate.Unavailable) + "."}
		if c.Whole {
			c.Detail = "Everyone wants to play."
		} else {
			c.Detail = fmt.Sprintf("%d want to play", len(candidate.Participants))
			if len(candidate.Unknown) > 0 {
				c.Detail += " · waiting on " + idsLabel(candidate.Unknown)
			}
			if len(candidate.Unavailable) > 0 {
				c.Detail += " · can't make the whole time: " + idsLabel(candidate.Unavailable)
			}
		}
		if c.Whole {
			view.Whole = append(view.Whole, c)
		} else {
			view.Near = append(view.Near, c)
		}
	}
	if len(view.Whole) > 3 {
		view.MoreWhole, view.Whole = view.Whole[3:], view.Whole[:3]
	}
	if len(view.Near) > 3 {
		view.MoreNear, view.Near = view.Near[3:], view.Near[:3]
	}
	proposals, err := s.st.Proposals(crew.ID, user.ID, now, now.Add(28*24*time.Hour))
	if err != nil {
		return nil, err
	}
	for _, p := range proposals {
		if p.Cancelled {
			continue
		}
		card := scheduleSessionVM{ID: p.ID, Title: p.Title, Label: scheduleTimeLabel(p.Start, p.End, loc), MyResponse: p.Responses[user.ID], CanCancel: p.CreatedBy == user.ID || mem.Can(store.RoleAdmin), Href: fmt.Sprintf("/c/%d/schedule#session-%d", crew.ID, p.ID)}
		if card.Title == "" {
			card.Title = "Game night"
		}
		for _, m := range members {
			response := p.Responses[m.ID]
			if response == "" {
				response = "pending"
			}
			card.Responses = append(card.Responses, scheduleResponseVM{Name: m.DisplayName, Response: response})
		}
		view.Sessions = append(view.Sessions, card)
	}
	return view, nil
}
func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	crew, mem, _, user, ok := s.crewContext(w, r)
	if !ok {
		return
	}
	view, err := s.scheduleView(r, crew, mem, user, r.URL.Query().Get("tz"), r.URL.Query().Get("duration"), nil)
	if err != nil {
		var conflict scheduleZoneConflict
		if errors.As(err, &conflict) {
			fallback, e := s.scheduleView(r, crew, mem, user, conflict.Zone, r.URL.Query().Get("duration"), nil)
			if e == nil {
				fallback.Error = conflict.Error()
				s.render(w, r, "schedule", http.StatusBadRequest, fallback)
				return
			}
			err = e
		}
		var input scheduleInputError
		if errors.As(err, &input) {
			s.fail(w, r, http.StatusBadRequest, "Could not open planning", err.Error())
		} else {
			s.log.Error("load planning", "err", err)
			s.fail(w, r, http.StatusInternalServerError, "Planning unavailable", "Could not load planning. Please try again.")
		}
		return
	}
	s.render(w, r, "schedule", http.StatusOK, view)
}

// All scheduling writes require a real form CSRF token, including JSON requests.
func (s *Server) schedulePost(w http.ResponseWriter, r *http.Request, sess store.Session) bool {
	return s.schedulePostLimit(w, r, sess, 32<<10)
}
func (s *Server) schedulePostLimit(w http.ResponseWriter, r *http.Request, sess store.Session, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.fail(w, r, http.StatusRequestEntityTooLarge, "Form too large", "Keep scheduling forms within the request size limit.")
		} else {
			s.fail(w, r, http.StatusBadRequest, "Could not read form", "Check the form values and try again.")
		}
		return false
	}
	if got := r.PostForm.Get("csrf"); got == "" || !subtleEqual(got, sess.CSRF) {
		s.fail(w, r, http.StatusForbidden, "Forbidden", "Your session token did not match. Reload and try again.")
		return false
	}
	return true
}
func scheduleReadFields(r *http.Request) scheduleFields {
	f := scheduleFields{Date: r.PostForm.Get("date"), Status: r.PostForm.Get("status"), Title: r.PostForm.Get("title"), ProposalStart: r.PostForm.Get("proposal_start"), ProposalEnd: r.PostForm.Get("proposal_end"), ProposalStartInstant: r.PostForm.Get("proposal_start_instant"), ProposalEndInstant: r.PostForm.Get("proposal_end_instant")}
	starts, ends := r.PostForm["start"], r.PostForm["end"]
	for i := 0; i < 8; i++ {
		w := scheduleWindowFields{Overnight: r.PostForm.Get(fmt.Sprintf("overnight%d", i)) == "1"}
		if i < len(starts) {
			w.Start = starts[i]
		}
		if i < len(ends) {
			w.End = ends[i]
		}
		f.Windows = append(f.Windows, w)
	}
	return f
}
func (s *Server) scheduleError(w http.ResponseWriter, r *http.Request, crew store.Crew, mem store.Membership, user store.User, err error) {
	if errors.Is(err, errScheduleInternal) {
		s.log.Error("save planning", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Planning unavailable", "Could not save planning. Please try again; your previous answers were not replaced.")
		return
	}
	message := err.Error()
	if errors.Is(err, store.ErrInvalid) {
		message = "Check your dates and times: availability must be today through the next 27 days; sessions must start in the future, end within 28 days, last at most 12 hours, and have a title of at most 160 characters without control characters."
	}
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
		message = "That session is not available in this crew, or you do not have permission."
	}
	if errors.Is(err, store.ErrExists) {
		message = "Your future availability uses another timezone, or the planning limit was reached. Clear future dated answers before changing timezone."
	}
	fields := scheduleReadFields(r)
	view, e := s.scheduleView(r, crew, mem, user, r.PostForm.Get("tz"), r.PostForm.Get("duration"), &fields)
	if e != nil {
		var input scheduleInputError
		var conflict scheduleZoneConflict
		if !errors.As(e, &input) && !errors.As(e, &conflict) {
			s.log.Error("redisplay planning form", "err", e)
			s.fail(w, r, http.StatusInternalServerError, "Planning unavailable", "Could not reload planning. Please try again.")
			return
		}
		s.fail(w, r, status, "Could not save planning", message)
		return
	}
	view.Error = message
	s.render(w, r, "schedule", status, view)
}
func (s *Server) scheduleSaved(w http.ResponseWriter, r *http.Request, crewID int64, message string) {
	s.redirect(w, r, scheduleURL(crewID, r.PostForm.Get("tz"), scheduleDuration(r.PostForm.Get("duration"))), message, "ok")
}
func (s *Server) handleScheduleAvailability(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, user, ok := s.crewContext(w, r)
	if !ok || !s.schedulePost(w, r, sess) {
		return
	}
	fields := scheduleReadFields(r)
	loc, err := scheduleZone(r.PostForm.Get("tz"))
	if err != nil {
		s.scheduleError(w, r, crew, mem, user, err)
		return
	}
	if fields.Status != "unknown" {
		if err := scheduleValidateDate(fields.Date, time.Now(), loc); err != nil {
			s.scheduleError(w, r, crew, mem, user, err)
			return
		}
	}
	if len(r.PostForm["start"]) > 8 || len(r.PostForm["end"]) > 8 {
		s.scheduleError(w, r, crew, mem, user, fmt.Errorf("use at most 8 windows per date"))
		return
	}
	switch fields.Status {
	case "unknown":
		err = scheduleStoreError(s.st.ClearAvailabilityDay(crew.ID, user.ID, fields.Date))
	case "unavailable":
		err = scheduleStoreError(s.st.SaveAvailabilityDay(crew.ID, user.ID, fields.Date, loc.String(), nil))
	case "available":
		var windows []schedule.Interval
		for _, f := range fields.Windows {
			if f.Start == "" && f.End == "" {
				continue
			}
			if f.Start == "" || f.End == "" {
				err = fmt.Errorf("each window needs both a start and end time")
				break
			}
			if (!f.Overnight && f.End <= f.Start) || (f.Overnight && f.End >= f.Start) {
				err = fmt.Errorf("end must be after start; for overnight windows tick ends next day and use an earlier end time")
				break
			}
			var window schedule.Interval
			window, err = schedule.LocalWindow(fields.Date, loc.String(), f.Start, f.End)
			if err != nil {
				break
			}
			windows = append(windows, window)
		}
		if err == nil && len(windows) == 0 {
			err = fmt.Errorf("add at least one window, or choose checked — unavailable")
		}
		if err == nil {
			err = scheduleStoreError(s.st.SaveAvailabilityDay(crew.ID, user.ID, fields.Date, loc.String(), windows))
		}
	default:
		err = fmt.Errorf("choose available, checked — unavailable, or clear answer")
	}
	if err != nil {
		s.scheduleError(w, r, crew, mem, user, err)
		return
	}
	s.scheduleSaved(w, r, crew.ID, "Your dated availability was saved.")
}

// scheduleHintedTime preserves an exact suggested instant only while its displayed
// wall time is unchanged. Editing the field restores strict manual DST validation.
func scheduleHintedTime(local, zone, hint string) (time.Time, error) {
	if len(local) != 16 || local[10] != 'T' {
		return time.Time{}, fmt.Errorf("enter both session dates and times")
	}
	loc, err := scheduleZone(zone)
	if err != nil {
		return time.Time{}, err
	}
	if instant, err := time.Parse(time.RFC3339, hint); err == nil && instant.Second() == 0 && instant.Nanosecond() == 0 && instant.In(loc).Format("2006-01-02T15:04") == local {
		return instant, nil
	}
	return schedule.LocalTime(local[:10], zone, local[11:])
}
func scheduleProposalTimes(start, end, zone string) (time.Time, time.Time, error) {
	return scheduleProposalTimesWithHints(start, end, zone, "", "")
}
func scheduleProposalTimesWithHints(start, end, zone, startHint, endHint string) (time.Time, time.Time, error) {
	a, err := scheduleHintedTime(start, zone, startHint)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	b, err := scheduleHintedTime(end, zone, endHint)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return a, b, nil
}
func (s *Server) handleScheduleProposal(w http.ResponseWriter, r *http.Request) {
	crew, mem, sess, user, ok := s.crewContext(w, r)
	if !ok || !s.schedulePost(w, r, sess) {
		return
	}
	loc, err := scheduleZone(r.PostForm.Get("tz"))
	if err != nil {
		s.scheduleError(w, r, crew, mem, user, err)
		return
	}
	start, end, err := scheduleProposalTimesWithHints(r.PostForm.Get("proposal_start"), r.PostForm.Get("proposal_end"), loc.String(), r.PostForm.Get("proposal_start_instant"), r.PostForm.Get("proposal_end_instant"))
	if err == nil {
		_, err = s.st.CreateProposal(crew.ID, user.ID, r.PostForm.Get("title"), start, end)
		err = scheduleStoreError(err)
	}
	if err != nil {
		s.scheduleError(w, r, crew, mem, user, err)
		return
	}
	s.scheduleSaved(w, r, crew.ID, "Session proposed. Ask the crew to RSVP.")
}
func (s *Server) handleScheduleRSVP(w http.ResponseWriter, r *http.Request) {
	s.scheduleSessionMutation(w, r, false)
}
func (s *Server) handleScheduleCancel(w http.ResponseWriter, r *http.Request) {
	s.scheduleSessionMutation(w, r, true)
}
func (s *Server) scheduleSessionMutation(w http.ResponseWriter, r *http.Request, cancel bool) {
	crew, mem, sess, user, ok := s.crewContext(w, r)
	if !ok || !s.schedulePost(w, r, sess) {
		return
	}
	if _, err := scheduleZone(r.PostForm.Get("tz")); err != nil {
		s.scheduleError(w, r, crew, mem, user, err)
		return
	}
	id, ok := idParam(r, "id")
	if !ok {
		s.notFound(w, r, "That session does not exist.")
		return
	}
	var err error
	if cancel {
		err = scheduleStoreError(s.st.CancelProposal(crew.ID, user.ID, id))
	} else {
		err = scheduleStoreError(s.st.SetProposalRSVP(crew.ID, user.ID, id, r.PostForm.Get("response")))
	}
	if err != nil {
		s.scheduleError(w, r, crew, mem, user, err)
		return
	}
	s.scheduleSaved(w, r, crew.ID, "Session updated.")
}
