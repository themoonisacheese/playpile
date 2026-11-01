package schedule_test

import (
	"math/rand"
	"testing"
	"time"

	"playpile/internal/schedule"
)

// Compare matching against a discrete independent oracle. A cell is either
// unknown, reviewed-unavailable, or reviewed-available for each member.
func TestSuggestionsDiscreteOracle(t *testing.T)      { testSuggestionsDiscreteOracle(t, false) }
func TestSuggestionsExplicitSlotsOracle(t *testing.T) { testSuggestionsDiscreteOracle(t, true) }

func testSuggestionsDiscreteOracle(t *testing.T, explicit bool) {
	const cells = 32
	const users = 4
	step := 15 * time.Minute
	origin := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(71241))
	for trial := 0; trial < 250; trial++ {
		state := [users][cells]int{}
		members := make([]schedule.MemberAvailability, users)
		for u := 0; u < users; u++ {
			members[u].UserID = int64(u + 1)
			for cell := 0; cell < cells; cell++ {
				// Long runs ensure nontrivial two-cell/full-crew intersections.
				if cell == 0 || rng.Intn(5) == 0 {
					state[u][cell] = rng.Intn(3)
				} else {
					state[u][cell] = state[u][cell-1]
				}
			}
			for kind := 1; kind <= 2; kind++ {
				for i := 0; i < cells; {
					matches := func(n int) bool {
						if kind == 1 {
							if explicit {
								return state[u][n] == 1
							}
							return state[u][n] != 0
						}
						return state[u][n] == 2
					}
					if !matches(i) {
						i++
						continue
					}
					end := i + 1
					for end < cells && matches(end) {
						end++
					}
					interval := schedule.Interval{Start: origin.Add(time.Duration(i) * step), End: origin.Add(time.Duration(end) * step)}
					if kind == 1 {
						if explicit {
							members[u].Blocked = append(members[u].Blocked, interval)
							// Add conflicting interest too: an explicit blocker must win.
							members[u].Windows = append(members[u].Windows, interval)
						} else {
							members[u].Reviewed = append(members[u].Reviewed, interval)
						}
					} else {
						members[u].Windows = append(members[u].Windows, interval)
					}
					i = end
				}
			}
		}
		got := schedule.Suggestions(members, origin, origin.Add(cells*step), 2*step, 256)
		covered := [cells]bool{}
		for _, s := range got {
			if s.End.Sub(s.Start) < 2*step || s.Start.Before(origin) || s.End.After(origin.Add(cells*step)) {
				t.Fatalf("trial %d invalid bounds %+v", trial, s)
			}
			first := int(s.Start.Sub(origin) / step)
			last := int(s.End.Sub(origin) / step)
			groups := []struct {
				ids  []int64
				want int
			}{{s.Participants, 2}, {s.Unknown, 0}, {s.Unavailable, 1}}
			seen := map[int64]bool{}
			for _, group := range groups {
				for _, id := range group.ids {
					if id < 1 || id > users || seen[id] {
						t.Fatalf("trial %d duplicate/invalid member %+v", trial, s)
					}
					seen[id] = true
					allYes, anyNo, anyUnknown := true, false, false
					for cell := first; cell < last; cell++ {
						allYes = allYes && state[id-1][cell] == 2
						anyNo = anyNo || state[id-1][cell] == 1
						anyUnknown = anyUnknown || state[id-1][cell] == 0
					}
					valid := (group.want == 2 && allYes) || (group.want == 1 && anyNo && !allYes) || (group.want == 0 && anyUnknown && !anyNo && !allYes)
					if !valid {
						t.Fatalf("trial %d user %d invalid full-window classification %d: %+v", trial, id, group.want, s)
					}
				}
			}
			if len(seen) != users || s.FullCrew != (len(s.Participants) == users) {
				t.Fatalf("trial %d inconsistent classifications %+v", trial, s)
			}
			if s.FullCrew {
				for cell := first; cell < last; cell++ {
					covered[cell] = true
				}
			}
		}
		for i := 0; i < cells; {
			full := func(cell int) bool {
				for u := 0; u < users; u++ {
					if state[u][cell] != 2 {
						return false
					}
				}
				return true
			}
			if !full(i) {
				i++
				continue
			}
			end := i + 1
			for end < cells && full(end) {
				end++
			}
			if end-i >= 2 {
				for cell := i; cell < end; cell++ {
					if !covered[cell] {
						t.Fatalf("trial %d missing full-crew cell %d", trial, cell)
					}
				}
			}
			i = end
		}
	}
}
