package schedule

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkSuggestions uses 14 days x two staggered four-hour windows per
// member, matching a scheduling page request rather than exponential subsets.
func BenchmarkSuggestions(b *testing.B) {
	origin := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range []int{10, 100} {
		b.Run(fmt.Sprintf("members%d", n), func(b *testing.B) {
			members := make([]MemberAvailability, n)
			for u := range members {
				members[u].UserID = int64(u + 1)
				for day := 0; day < 14; day++ {
					for window := 0; window < 2; window++ {
						start := origin.Add(time.Duration(day)*24*time.Hour + time.Duration(window)*8*time.Hour + time.Duration(u)*time.Minute)
						members[u].Windows = append(members[u].Windows, Interval{Start: start, End: start.Add(4 * time.Hour)})
					}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result := Suggestions(members, origin, origin.Add(14*24*time.Hour), 2*time.Hour, 24)
				if len(result) != 24 {
					b.Fatalf("results=%d", len(result))
				}
			}
		})
	}
}
