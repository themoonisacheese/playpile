package web

import (
	"fmt"
	"sort"
	"time"

	"playpile/internal/schedule"
	"playpile/internal/store"
)

// scheduleCrewTimetable projects only the already-authorized, bounded page rows.
// Roster order follows Members; cells refer to it by one byte per other member.
// Canonicalization happens once per member, never per cell or viewer date.
func scheduleCrewTimetable(week []scheduleDayVM, members []store.Member, actor int64, saved []store.AvailabilityDay, zone string) ([]scheduleCrewMemberVM, error) {
	loc, err := scheduleZone(zone)
	if err != nil {
		return nil, err
	}
	type cellProjection struct {
		slot     *scheduleSlotVM
		interval schedule.Interval
		states   []byte
		counts   [4]int
	}
	var cells []cellProjection
	for i := range week {
		for j := range week[i].Slots {
			slot := &week[i].Slots[j]
			start, e := time.Parse(time.RFC3339, slot.Start)
			if e != nil {
				return nil, e
			}
			end, e := time.Parse(time.RFC3339, slot.End)
			if e != nil {
				return nil, e
			}
			slot.CrewBars = nil
			cells = append(cells, cellProjection{slot: slot, interval: schedule.Interval{Start: start, End: end}})
		}
	}
	var roster []scheduleCrewMemberVM
	if len(cells) == 0 {
		return roster, nil
	}
	bounds := schedule.Interval{Start: cells[0].interval.Start, End: cells[len(cells)-1].interval.End}
	rows := make(map[int64][]store.AvailabilityDay)
	for _, d := range saved {
		if d.UserID != actor {
			rows[d.UserID] = append(rows[d.UserID], d)
		}
	}
	labels := func(intervals []schedule.Interval) []string {
		var out []string
		for _, w := range intervals {
			format := func(t time.Time) string {
				layout := "Mon 02 Jan 2006, 15:04 -07:00"
				if t.Second() != 0 || t.Nanosecond() != 0 {
					layout = "Mon 02 Jan 2006, 15:04:05.999999999 -07:00"
				}
				return t.In(loc).Format(layout)
			}
			out = append(out, format(w.Start)+" → "+format(w.End))
		}
		return out
	}
	for _, member := range members {
		if member.ID == actor {
			continue
		}
		canonical, e := store.CanonicalAvailabilityDays(rows[member.ID])
		if e != nil {
			return nil, e
		}
		var wants, blocked []schedule.Interval
		for _, d := range canonical {
			wants = append(wants, d.Windows...)
			blocked = append(blocked, d.Blocked...)
		}
		// Union adjacent source-day pieces, including cells straddling another
		// member's midnight in a quarter-hour-offset zone.
		wants, blocked = schedule.CanonicalIntervals(wants, blocked, nil, bounds)
		roster = append(roster, scheduleCrewMemberVM{Name: member.DisplayName, Wants: labels(wants), Blocked: labels(blocked)})
		for i := range cells {
			c := &cells[i]
			fw, aw := scheduleCrewCoverage(wants, c.interval)
			fb, ab := scheduleCrewCoverage(blocked, c.interval)
			state, code, count := "unknown", byte('u'), 2
			if fb {
				state, code, count = "cant", 'c', 1
			} else if fw && !ab {
				state, code, count = "want", 'w', 0
			} else if aw || ab {
				state, code, count = "mixed", 'm', 3
			}
			c.states = append(c.states, code)
			c.counts[count]++
			if len(roster) <= 4 {
				c.slot.CrewBars = append(c.slot.CrewBars, state)
			}
		}
	}
	for _, c := range cells {
		c.slot.CrewStates = string(c.states)
		c.slot.CrewMore = max(0, len(roster)-4)
		c.slot.CrewSummary = fmt.Sprintf("Crew: %d want to play, %d can't play, %d unknown, %d partial/mixed", c.counts[0], c.counts[1], c.counts[2], c.counts[3])
	}
	return roster, nil
}

// Canonical intervals are sorted and disjoint: the first possible overlap
// suffices for full/any coverage. No scan grows with saved history per cell.
func scheduleCrewCoverage(intervals []schedule.Interval, cell schedule.Interval) (full, any bool) {
	i := sort.Search(len(intervals), func(i int) bool { return intervals[i].End.After(cell.Start) })
	if i == len(intervals) || !intervals[i].Start.Before(cell.End) {
		return false, false
	}
	w := intervals[i]
	return !w.Start.After(cell.Start) && !w.End.Before(cell.End), true
}
