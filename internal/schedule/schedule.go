// Package schedule contains pure civil-time conversion and overlap arithmetic.
package schedule

import (
	"errors"
	"sort"
	"strconv"
	"time"
	_ "time/tzdata" // Static deployments must not depend on host zoneinfo.
)

type Interval struct{ Start, End time.Time }
type MemberAvailability struct {
	UserID            int64
	Windows, Reviewed []Interval
}
type Suggestion struct {
	Start, End                         time.Time
	Participants, Unknown, Unavailable []int64
	FullCrew                           bool
}

// DayBounds returns the actual civil day (which need not be 24 hours).
func DayBounds(date, zone string) (Interval, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return Interval{}, errors.New("invalid IANA timezone")
	}
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return Interval{}, err
	}
	// Search the first instant on/after the civil date, rather than relying on
	// time.Date's unspecified choice for a skipped midnight (e.g. Santiago).
	boundary := func(day time.Time) time.Time {
		target := day.Format("2006-01-02")
		lo, hi := day.Add(-48*time.Hour).Unix(), day.Add(48*time.Hour).Unix()
		for lo < hi {
			mid := lo + (hi-lo)/2
			if time.Unix(mid, 0).In(loc).Format("2006-01-02") < target {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return time.Unix(lo, 0).In(loc)
	}
	start := boundary(d)
	end := boundary(d.AddDate(0, 0, 1))
	if start.Format("2006-01-02") != date || !end.After(start) {
		return Interval{}, errors.New("nonexistent civil date")
	}
	return Interval{start.UTC(), end.UTC()}, nil
}

// wall refuses both nonexistent and repeated wall minutes: choose an unambiguous
// adjacent time instead of silently picking one occurrence during DST fallback.
func wall(date, clock string, loc *time.Location) (time.Time, error) {
	d, err := time.Parse("2006-01-02 15:04", date+" "+clock)
	if err != nil {
		return time.Time{}, err
	}
	// Collect every UTC offset around this date, including historical half-hour shifts.
	offsets := map[int]bool{}
	for h := -48; h <= 48; h++ {
		_, off := d.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[off] = true
	}
	var found []time.Time
	for off := range offsets {
		candidate := d.Add(-time.Duration(off) * time.Second)
		if candidate.In(loc).Format("2006-01-02 15:04") == date+" "+clock {
			found = append(found, candidate)
		}
	}
	if len(found) != 1 {
		return time.Time{}, errors.New("nonexistent or ambiguous local time")
	}
	return found[0].UTC(), nil
}

// LocalTime resolves exactly one local wall minute, rejecting DST gaps/repeats.
func LocalTime(date, zone, clock string) (time.Time, error) {
	if _, err := DayBounds(date, zone); err != nil {
		return time.Time{}, err
	}
	loc, _ := time.LoadLocation(zone)
	return wall(date, clock, loc)
}

// LocalWindow interprets an earlier end as next-day. Equal clocks are invalid.
func LocalWindow(date, zone, start, end string) (Interval, error) {
	if _, err := DayBounds(date, zone); err != nil {
		return Interval{}, err
	}
	loc, _ := time.LoadLocation(zone)
	if start == end {
		return Interval{}, errors.New("equal start and end")
	}
	a, err := wall(date, start, loc)
	if err != nil {
		return Interval{}, err
	}
	endDate := date
	if end < start {
		d, _ := time.Parse("2006-01-02", date)
		endDate = d.AddDate(0, 0, 1).Format("2006-01-02")
	}
	b, err := wall(endDate, end, loc)
	if err != nil {
		return Interval{}, err
	}
	if !b.After(a) || b.Sub(a) > 24*time.Hour {
		return Interval{}, errors.New("invalid window duration")
	}
	return Interval{a, b}, nil
}

// covering finds a single normalized union interval covering [a,b]. Normalized
// windows are sorted, nonoverlapping, and have strictly increasing ends.
func covering(windows []Interval, a, b time.Time) (Interval, bool) {
	index := sort.Search(len(windows), func(i int) bool { return !windows[i].End.Before(b) })
	if index < len(windows) && !windows[index].Start.After(a) {
		return windows[index], true
	}
	return Interval{}, false
}

func covers(windows []Interval, a, b time.Time) bool { _, ok := covering(windows, a, b); return ok }

// Suggestions returns continuous windows with fixed participant identities.
// Participants can attend the entire span. Unavailable means some reviewed
// portion definitely blocks attendance for the whole span; Unknown means no
// definite blocker, but attendance for the whole span is not known.
//
// Candidate anchors are endpoints and end-minus-minimum, not all subsets of a
// crew. This deliberately bounded heuristic is not exhaustive subset search.
// At most 100 suggestions are returned, full crew first, then larger groups.
func Suggestions(members []MemberAvailability, from, to time.Time, min time.Duration, limit int) []Suggestion {
	if !to.After(from) || to.Sub(from) > 28*24*time.Hour || min <= 0 || limit <= 0 || len(members) == 0 {
		return nil
	}
	if limit > 100 {
		limit = 100
	}
	members = append([]MemberAvailability(nil), members...)
	sort.Slice(members, func(i, j int) bool { return members[i].UserID < members[j].UserID })
	anchors := []time.Time{from, to.Add(-min)}
	for i := range members {
		members[i].Windows = normalize(members[i].Windows, from, to)
		members[i].Reviewed = normalize(members[i].Reviewed, from, to)
		for _, w := range members[i].Windows {
			anchors = append(anchors, w.Start, w.End.Add(-min))
		}
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].Before(anchors[j]) })
	// Group-local containment avoids comparing every candidate against all other
	// crews' participant tuples. Reuse the encoding buffer; string keys own copies.
	groups := make(map[string][]Suggestion)
	keyBuffer := make([]byte, 0, len(members)*8)
	for index, anchor := range anchors {
		if anchor.Before(from) || anchor.Add(min).After(to) || (index > 0 && anchor.Equal(anchors[index-1])) {
			continue
		}
		s := Suggestion{Start: from, End: to}
		endMinimum := anchor.Add(min)
		for _, m := range members {
			if w, ok := covering(m.Windows, anchor, endMinimum); ok {
				s.Participants = append(s.Participants, m.UserID)
				if w.Start.After(s.Start) {
					s.Start = w.Start
				}
				if w.End.Before(s.End) {
					s.End = w.End
				}
			}
		}
		required := 2
		if len(members) == 1 {
			required = 1
		}
		if len(s.Participants) < required {
			continue
		}
		keyBuffer = keyBuffer[:0]
		for _, id := range s.Participants {
			keyBuffer = strconv.AppendInt(keyBuffer, id, 10)
			keyBuffer = append(keyBuffer, ',')
		}
		key := string(keyBuffer)
		candidates := groups[key]
		duplicate := false
		for _, old := range candidates {
			if !s.Start.Before(old.Start) && !s.End.After(old.End) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		// Only same-group candidates can contain each other. Distinct identities
		// remain separate even when their intervals are identical.
		kept := candidates[:0]
		for _, old := range candidates {
			if !old.Start.Before(s.Start) && !old.End.After(s.End) {
				continue
			}
			kept = append(kept, old)
		}
		s.FullCrew = len(s.Participants) == len(members)
		groups[key] = append(kept, s)
	}
	var out []Suggestion
	for _, candidates := range groups {
		out = append(out, candidates...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FullCrew != out[j].FullCrew {
			return out[i].FullCrew
		}
		if len(out[i].Participants) != len(out[j].Participants) {
			return len(out[i].Participants) > len(out[j].Participants)
		}
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		if !out[i].End.Equal(out[j].End) {
			return out[i].End.Before(out[j].End)
		}
		for k := range out[i].Participants {
			if out[i].Participants[k] != out[j].Participants[k] {
				return out[i].Participants[k] < out[j].Participants[k]
			}
		}
		return false
	})
	if len(out) > limit {
		out = out[:limit]
	}
	// Classifications do not affect ranking; compute them only for results
	// that the caller will render, not for every candidate discovered above.
	for i := range out {
		s := &out[i]
		for _, m := range members {
			idx := sort.Search(len(s.Participants), func(i int) bool { return s.Participants[i] >= m.UserID })
			if idx < len(s.Participants) && s.Participants[idx] == m.UserID {
				continue
			}
			blocked := false
			for _, r := range m.Reviewed {
				a, b := r.Start, r.End
				if a.Before(s.Start) {
					a = s.Start
				}
				if b.After(s.End) {
					b = s.End
				}
				if b.After(a) && !covers(m.Windows, a, b) {
					blocked = true
					break
				}
			}
			if blocked {
				s.Unavailable = append(s.Unavailable, m.UserID)
			} else {
				s.Unknown = append(s.Unknown, m.UserID)
			}
		}
	}
	return out
}

// normalize copies, clips, sorts and unions adjacent intervals once per member.
func normalize(input []Interval, from, to time.Time) []Interval {
	var sorted []Interval
	for _, w := range input {
		if w.Start.Before(from) {
			w.Start = from
		}
		if w.End.After(to) {
			w.End = to
		}
		if w.End.After(w.Start) {
			sorted = append(sorted, w)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })
	var out []Interval
	for _, w := range sorted {
		if len(out) > 0 && !w.Start.After(out[len(out)-1].End) {
			if w.End.After(out[len(out)-1].End) {
				out[len(out)-1].End = w.End
			}
		} else {
			out = append(out, w)
		}
	}
	return out
}
