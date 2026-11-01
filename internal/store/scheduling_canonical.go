package store

import (
	"database/sql"
	"encoding/json"
	"playpile/internal/schedule"
	"sort"
	"time"
)

// CanonicalAvailabilityDays projects one member's saved rows into sparse civil
// days without changing the store. Include every source row whose spillover can
// reach the displayed range. This preserves old positive-over-implicit-no rules,
// while explicit blockers always win. No rows for other members may be mixed in.
func CanonicalAvailabilityDays(days []AvailabilityDay) ([]AvailabilityDay, error) {
	if len(days) == 0 {
		return nil, nil
	}
	user, zone := days[0].UserID, days[0].Zone
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, ErrInvalid
	}
	var wants, blocked, reviewed []schedule.Interval
	bounds := map[string]schedule.Interval{}
	for _, d := range days {
		if d.UserID != user || d.Zone != zone {
			return nil, ErrInvalid
		}
		b, err := schedule.DayBounds(d.Date, zone)
		if err != nil {
			return nil, ErrInvalid
		}
		bounds[d.Date] = b
		if !d.Explicit {
			reviewed = append(reviewed, b)
		}
		wants = append(wants, d.Windows...)
		blocked = append(blocked, d.Blocked...)
		for _, list := range [][]schedule.Interval{d.Windows, d.Blocked} {
			for _, w := range list {
				if !w.End.After(w.Start) {
					return nil, ErrInvalid
				}
				// Saved windows are bounded to at most one civil day or 24 elapsed
				// hours; reject corrupt inputs rather than growing without bound.
				if w.End.Sub(w.Start) > 26*time.Hour {
					return nil, ErrInvalid
				}
				for start := w.Start; start.Before(w.End); {
					date := start.In(loc).Format("2006-01-02")
					day, err := schedule.DayBounds(date, zone)
					if err != nil {
						return nil, ErrInvalid
					}
					bounds[date] = day
					start = day.End
				}
			}
		}
	}
	dates := make([]string, 0, len(bounds))
	for d := range bounds {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	var out []AvailabilityDay
	for _, date := range dates {
		w, b := schedule.CanonicalIntervals(wants, blocked, reviewed, bounds[date])
		if len(w) == 0 && len(b) == 0 {
			continue
		}
		out = append(out, AvailabilityDay{UserID: user, Date: date, Zone: zone, Windows: w, Blocked: b, Explicit: true})
	}
	return out, nil
}

// canonicalizeAvailability rewrites only this actor's rows, only when legacy
// rows exist, inside the caller's already authorized transaction.
func canonicalizeAvailability(tx *sql.Tx, crewID, actorID int64) ([]availabilityWrite, error) {
	rows, err := tx.Query("SELECT date,zone,windows,blocked FROM availability_days WHERE crew_id=? AND user_id=? ORDER BY date", crewID, actorID)
	if err != nil {
		return nil, err
	}
	var days []AvailabilityDay
	legacy := false
	for rows.Next() {
		d := AvailabilityDay{UserID: actorID}
		var w, b string
		if err := rows.Scan(&d.Date, &d.Zone, &w, &b); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(w), &d.Windows); err != nil {
			rows.Close()
			return nil, err
		}
		d.Explicit = b != ""
		if d.Explicit {
			if err := json.Unmarshal([]byte(b), &d.Blocked); err != nil {
				rows.Close()
				return nil, err
			}
		} else {
			legacy = true
		}
		days = append(days, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if !legacy {
		return nil, nil
	}
	canonical, err := CanonicalAvailabilityDays(days)
	if err != nil {
		return nil, err
	}
	var out []availabilityWrite
	for _, d := range canonical {
		bounds, err := schedule.DayBounds(d.Date, d.Zone)
		if err != nil {
			return nil, err
		}
		if bounds.End.Unix() <= now() {
			continue
		}
		w, err := json.Marshal(d.Windows)
		if err != nil {
			return nil, err
		}
		if d.Blocked == nil {
			d.Blocked = []schedule.Interval{}
		}
		b, err := json.Marshal(d.Blocked)
		if err != nil {
			return nil, err
		}
		out = append(out, availabilityWrite{date: d.Date, bounds: bounds, expiry: bounds.End.Unix(), windows: string(w), blocked: string(b)})
	}
	if _, err := tx.Exec("DELETE FROM availability_days WHERE crew_id=? AND user_id=?", crewID, actorID); err != nil {
		return nil, err
	}
	return out, nil
}
