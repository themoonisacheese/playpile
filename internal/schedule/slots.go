package schedule

import "time"

// subtract removes sorted disjoint blockers from sorted disjoint windows in
// linear time. Its result remains sorted and disjoint.
func subtract(windows, blocked []Interval) []Interval {
	var out []Interval
	j := 0
	for _, w := range windows {
		start := w.Start
		for j < len(blocked) && !blocked[j].End.After(start) {
			j++
		}
		for k := j; k < len(blocked) && blocked[k].Start.Before(w.End); k++ {
			b := blocked[k]
			if b.Start.After(start) {
				out = append(out, Interval{start, b.Start})
			}
			if b.End.After(start) {
				start = b.End
			}
			if !start.Before(w.End) {
				break
			}
		}
		if start.Before(w.End) {
			out = append(out, Interval{start, w.End})
		}
	}
	return out
}

func overlaps(windows []Interval, start, end time.Time) bool {
	// Linear scan is bounded by the saved planning horizon and performed only
	// for the final limited suggestion set.
	for _, w := range windows {
		if !w.Start.Before(end) {
			break
		}
		if w.End.After(start) {
			return true
		}
	}
	return false
}

// LegacyBlocked returns the reviewed civil-day portion not covered by any wants.
func LegacyBlocked(bounds Interval, wants []Interval) []Interval {
	return subtract([]Interval{bounds}, normalize(wants, bounds.Start, bounds.End))
}

// PaintIntervals overwrites one validated cell, retaining every untouched edge.
// States are "want", "blocked", and "erase". Unknown states leave input unchanged.
func PaintIntervals(wants, blocked []Interval, cell Interval, state string) ([]Interval, []Interval) {
	if !cell.End.After(cell.Start) || (state != "want" && state != "blocked" && state != "erase") {
		return wants, blocked
	}
	all := append(append([]Interval{}, wants...), blocked...)
	from, to := cell.Start, cell.End
	for _, w := range all {
		if w.Start.Before(from) {
			from = w.Start
		}
		if w.End.After(to) {
			to = w.End
		}
	}
	wants = subtract(normalize(wants, from, to), []Interval{cell})
	blocked = subtract(normalize(blocked, from, to), []Interval{cell})
	if state == "want" {
		wants = append(wants, cell)
	}
	if state == "blocked" {
		blocked = append(blocked, cell)
	}
	return normalize(wants, from, to), normalize(blocked, from, to)
}

// CanonicalIntervals resolves legacy reviewed days and explicit sparse answers
// inside bounds. Any positive beats implicit legacy no; explicit blockers beat
// every positive. The returned lists are normalized, disjoint and clipped.
func CanonicalIntervals(wants, blocked, reviewed []Interval, bounds Interval) ([]Interval, []Interval) {
	wants = normalize(wants, bounds.Start, bounds.End)
	blocked = normalize(blocked, bounds.Start, bounds.End)
	reviewed = normalize(reviewed, bounds.Start, bounds.End)
	implicit := subtract(reviewed, wants)
	positive := subtract(wants, blocked)
	negative := normalize(append(implicit, blocked...), bounds.Start, bounds.End)
	return positive, negative
}
