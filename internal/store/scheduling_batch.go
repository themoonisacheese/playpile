package store

import "playpile/internal/schedule"

// AvailabilitySlotDay contains only the authenticated actor's dated answer.
type AvailabilitySlotDay struct {
	Date           string
	Wants, Blocked []schedule.Interval
}

// SaveAvailabilitySlotsBatch atomically saves at most seven unique civil days.
// Empty lists erase a day to unknown. Untouched dates are never rewritten.
func (s *Store) SaveAvailabilitySlotsBatch(crewID, actorID int64, zone string, days []AvailabilitySlotDay) error {
	if len(days) == 0 || len(days) > 7 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	rows := make([]availabilityWrite, 0, len(days))
	for _, d := range days {
		if seen[d.Date] {
			return ErrInvalid
		}
		seen[d.Date] = true
		row, err := validateAvailability(d.Date, zone, d.Wants, d.Blocked, true)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	return s.writeAvailability(crewID, actorID, zone, rows, true)
}
