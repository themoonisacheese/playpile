package store

import (
	"encoding/json"
	"errors"
	"playpile/internal/schedule"
	"reflect"
	"testing"
	"time"
)

func TestAvailabilitySlotsSaveEraseAndValidation(t *testing.T) {
	s, c, o, m, x := scheduleFixture(t)
	date := futureDay()
	cells, err := schedule.DaySlots(date, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	wants, blocked := cells[12:14], cells[20:22]
	save := func(w, b []schedule.Interval) error { return s.SaveAvailabilitySlots(c.ID, m.ID, date, "UTC", w, b) }
	if err := save(wants, blocked); err != nil {
		t.Fatal(err)
	}
	original := readDays(t, s, c.ID, o.ID)
	if len(original) != 1 || !original[0].Explicit || !reflect.DeepEqual(original[0].Windows, wants) || !reflect.DeepEqual(original[0].Blocked, blocked) {
		t.Fatalf("slot state %+v", original)
	}
	invalid := []struct{ wants, blocked []schedule.Interval }{
		{wants, wants}, {nil, []schedule.Interval{{Start: cells[0].Start.Add(-time.Minute), End: cells[0].End}}},
		{[]schedule.Interval{{Start: cells[47].Start, End: cells[47].End.Add(time.Minute)}}, nil},
		{nil, []schedule.Interval{{Start: cells[0].Start.Add(time.Nanosecond), End: cells[0].End}}},
		{[]schedule.Interval{{Start: cells[0].End, End: cells[0].Start}}, nil},
		{make([]schedule.Interval, 97), nil}, {nil, make([]schedule.Interval, 97)},
	}
	for i, v := range invalid {
		if err := save(v.wants, v.blocked); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid %d: %v", i, err)
		}
		if got := readDays(t, s, c.ID, o.ID); !reflect.DeepEqual(got, original) {
			t.Fatalf("invalid %d changed previous data %+v", i, got)
		}
	}
	if err := s.SaveAvailabilitySlots(c.ID, x.ID, date, "UTC", wants, blocked); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider %v", err)
	}
	other, err := s.CreateCrew("Other slots", "US", "english", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAvailabilitySlots(other.ID, m.ID, date, "UTC", wants, blocked); !errors.Is(err, ErrNotFound) {
		t.Fatalf("crosscrew %v", err)
	}
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, date, "Europe/Paris", nil, nil); !errors.Is(err, ErrExists) {
		t.Fatalf("zone lock %v", err)
	}
	if err := save(nil, nil); err != nil {
		t.Fatal(err)
	}
	got := readDays(t, s, c.ID, o.ID)
	if len(got) != 0 {
		t.Fatalf("blank should stay unknown %+v", got)
	}
	if err := s.ClearAvailabilityDay(c.ID, m.ID, date); err != nil {
		t.Fatal(err)
	}
	if got := readDays(t, s, c.ID, o.ID); len(got) != 0 {
		t.Fatalf("erase %+v", got)
	}
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, date, "Europe/Paris", nil, nil); err != nil {
		t.Fatalf("zone not unlocked %v", err)
	}
}

func TestAvailabilitySlotsUpgradeOldRowsAndReopen(t *testing.T) {
	dir := t.TempDir()
	old := &Store{db: openRawDB(t, dir)}
	// schedulingSchema intentionally describes the schema before the additive
	// blocked migration, providing a real pre-slot database with legacy data.
	if _, err := old.db.Exec(schema + schedulingSchema); err != nil {
		t.Fatal(err)
	}
	o := newUser(t, old, "slot-migration", "Owner")
	c, err := old.CreateCrew("Before slots", "US", "english", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	date := futureDay()
	bounds, _ := schedule.DayBounds(date, "UTC")
	overnight := futureWindow(t, date)
	raw, _ := json.Marshal([]schedule.Interval{overnight})
	if _, err := old.db.Exec("INSERT INTO availability_days(crew_id,user_id,date,zone,start_at,end_at,expires_at,windows) VALUES(?,?,?,?,?,?,?,?)", c.ID, o.ID, date, "UTC", bounds.Start.Unix(), bounds.End.Unix(), overnight.End.Unix(), string(raw)); err != nil {
		t.Fatal(err)
	}
	p, err := old.CreateProposal(c.ID, o.ID, "Keep proposal", overnight.Start, overnight.End)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.SetProposalRSVP(c.ID, o.ID, p.ID, "maybe"); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for i := 0; i < 2; i++ {
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		days := readDays(t, s, c.ID, o.ID)
		if len(days) != 1 || days[0].Explicit || len(days[0].Blocked) != 0 || !days[0].Windows[0].End.Equal(overnight.End) {
			t.Fatalf("legacy row %+v", days)
		}
		proposals, err := s.Proposals(c.ID, o.ID, bounds.Start, overnight.End)
		if err != nil || len(proposals) != 1 || proposals[0].Responses[o.ID] != "maybe" {
			t.Fatalf("proposals changed %+v %v", proposals, err)
		}
		if i == 1 {
			if err := s.SaveAvailabilitySlots(c.ID, o.ID, date, "UTC", nil, []schedule.Interval{bounds}); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	days := readDays(t, s, c.ID, o.ID)
	if len(days) != 2 || !days[0].Explicit || len(days[0].Blocked) != 1 || !days[1].Explicit || len(days[1].Windows) != 1 {
		t.Fatalf("new row failed reopen %+v", days)
	}
}

func TestAvailabilitySlotsBatchAtomicAndBounded(t *testing.T) {
	s, c, o, m, _ := scheduleFixture(t)
	date := futureDay()
	base, _ := time.Parse("2006-01-02", date)
	next := base.AddDate(0, 0, 1).Format("2006-01-02")
	cells, _ := schedule.DaySlots(date, "UTC")
	nextCells, _ := schedule.DaySlots(next, "UTC")
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, date, "UTC", cells[0:2], nil); err != nil {
		t.Fatal(err)
	}
	before := readDays(t, s, c.ID, o.ID)
	invalid := []AvailabilitySlotDay{
		{Date: date, Blocked: cells[2:4]},
		{Date: next, Wants: nextCells[0:2], Blocked: nextCells[1:3]},
	}
	if err := s.SaveAvailabilitySlotsBatch(c.ID, m.ID, "UTC", invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid day %v", err)
	}
	if after := readDays(t, s, c.ID, o.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("partial invalid save %+v", after)
	}
	for _, days := range [][]AvailabilitySlotDay{nil, {{Date: date}, {Date: date}}, make([]AvailabilitySlotDay, 8)} {
		if err := s.SaveAvailabilitySlotsBatch(c.ID, m.ID, "UTC", days); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad batch %v", err)
		}
	}
	if err := s.SaveAvailabilitySlotsBatch(c.ID, m.ID, "UTC", []AvailabilitySlotDay{{Date: date}, {Date: next, Wants: nextCells[2:4]}}); err != nil {
		t.Fatal(err)
	}
	after := readDays(t, s, c.ID, o.ID)
	if len(after) != 1 || after[0].Date != next || !after[0].Explicit {
		t.Fatalf("batch erase/save %+v", after)
	}
	// Updating one date does not reinterpret the unsubmitted other date.
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, date, "UTC", nil, cells[0:2]); err != nil {
		t.Fatal(err)
	}
	after = readDays(t, s, c.ID, o.ID)
	if len(after) != 2 || !reflect.DeepEqual(after[1].Windows, nextCells[2:4]) {
		t.Fatalf("untouched day lost %+v", after)
	}
	// Slot rows use the same membership foreign key and disappear on removal.
	if err := s.RemoveMember(c.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if got := readDays(t, s, c.ID, o.ID); len(got) != 0 {
		t.Fatalf("slot cascade %+v", got)
	}
}

func TestAvailabilitySlotsAcceptsMinutePrecisionAnd96Intervals(t *testing.T) {
	s, c, o, m, _ := scheduleFixture(t)
	date := futureDay()
	bounds, _ := schedule.DayBounds(date, "UTC")
	var wants, blocked []schedule.Interval
	for i := 0; i < 96; i++ {
		start := bounds.Start.Add(time.Duration(i*4) * time.Minute)
		wants = append(wants, schedule.Interval{Start: start, End: start.Add(time.Minute)})
		blocked = append(blocked, schedule.Interval{Start: start.Add(2 * time.Minute), End: start.Add(3 * time.Minute)})
	}
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, date, "UTC", wants, blocked); err != nil {
		t.Fatal(err)
	}
	got := readDays(t, s, c.ID, o.ID)
	if len(got) != 1 || len(got[0].Windows) != 96 || len(got[0].Blocked) != 96 {
		t.Fatalf("max interval count %+v", got)
	}
}

func TestSlotsBatchCanonicalizesLegacyAndErasesNextDayTail(t *testing.T) {
	s, c, o, m, _ := scheduleFixture(t)
	date := futureDay()
	base, _ := time.Parse("2006-01-02", date)
	next := base.AddDate(0, 0, 1).Format("2006-01-02")
	later := base.AddDate(0, 0, 2).Format("2006-01-02")
	overnight := futureWindow(t, date)
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "UTC", []schedule.Interval{overnight}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAvailabilityDay(c.ID, m.ID, next, "UTC", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAvailabilityDay(c.ID, o.ID, date, "UTC", []schedule.Interval{overnight}); err != nil {
		t.Fatal(err)
	}
	original := readDays(t, s, c.ID, o.ID)
	cells, _ := schedule.DaySlots(later, "UTC")
	// The invalid second day must not even convert the first member's legacy rows.
	if err := s.SaveAvailabilitySlotsBatch(c.ID, m.ID, "UTC", []AvailabilitySlotDay{{Date: next}, {Date: later, Wants: cells[:1], Blocked: cells[:1]}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid batch %v", err)
	}
	if got := readDays(t, s, c.ID, o.ID); !reflect.DeepEqual(got, original) {
		t.Fatalf("invalid converted data %+v", got)
	}
	var own []AvailabilityDay
	for _, d := range original {
		if d.UserID == m.ID {
			own = append(own, d)
		}
	}
	canonical, err := CanonicalAvailabilityDays(own)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 2 || len(canonical[1].Windows) != 1 || !canonical[1].Windows[0].Start.Equal(base.Add(24*time.Hour)) {
		t.Fatalf("old positive must beat implicit no %+v", canonical)
	}
	// Erase the exact next-day tail, retaining legacy reviewed no elsewhere.
	tail := schedule.Interval{Start: base.Add(24 * time.Hour), End: overnight.End}
	wants, blocked := schedule.PaintIntervals(canonical[1].Windows, canonical[1].Blocked, tail, "erase")
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, next, "UTC", wants, blocked); err != nil {
		t.Fatal(err)
	}
	got := readDays(t, s, c.ID, o.ID)
	count := 0
	for _, d := range got {
		if d.UserID == o.ID {
			if d.Explicit || !d.Windows[0].End.Equal(overnight.End) {
				t.Fatalf("other actor changed %+v", d)
			}
			continue
		}
		count++
		if !d.Explicit {
			t.Fatalf("legacy row remained %+v", d)
		}
		if d.Date == date && (len(d.Windows) != 1 || !d.Windows[0].End.Equal(base.Add(24*time.Hour))) {
			t.Fatalf("source not split %+v", d)
		}
		if d.Date == next && (len(d.Windows) != 0 || len(d.Blocked) != 1 || !d.Blocked[0].Start.Equal(overnight.End)) {
			t.Fatalf("erased tail remained %+v", d)
		}
	}
	if count != 2 {
		t.Fatalf("missing canonical day %+v", got)
	}
	// Erasing every mark on the target day deletes it; no previous source tail revives.
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, next, "UTC", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range readDays(t, s, c.ID, o.ID) {
		if d.UserID == m.ID && d.Date == next {
			t.Fatalf("erased day revived %+v", d)
		}
	}
}

func TestCanonicalAvailabilityExplicitBlockWinsAcrossLegacySpill(t *testing.T) {
	b, _ := schedule.DayBounds("2030-01-01", "UTC")
	next, _ := schedule.DayBounds("2030-01-02", "UTC")
	spill := schedule.Interval{Start: b.End.Add(-time.Hour), End: b.End.Add(2 * time.Hour)}
	no := schedule.Interval{Start: next.Start.Add(time.Hour), End: next.Start.Add(3 * time.Hour)}
	days := []AvailabilityDay{
		{UserID: 1, Date: "2030-01-01", Zone: "UTC", Windows: []schedule.Interval{spill}},
		{UserID: 1, Date: "2030-01-02", Zone: "UTC", Explicit: true, Blocked: []schedule.Interval{no}},
	}
	got, err := CanonicalAvailabilityDays(days)
	if err != nil || len(got) != 2 {
		t.Fatalf("canonical %+v %v", got, err)
	}
	if !reflect.DeepEqual(got[1].Windows, []schedule.Interval{{Start: next.Start, End: no.Start}}) || !reflect.DeepEqual(got[1].Blocked, []schedule.Interval{no}) {
		t.Fatalf("explicit no lost %+v", got[1])
	}
	days[1].UserID = 2
	if _, err := CanonicalAvailabilityDays(days); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mixed actors %v", err)
	}
}

func TestSlotsPreserveLegacySecondsDuringUnrelatedPaint(t *testing.T) {
	s, c, o, m, _ := scheduleFixture(t)
	date := futureDay()
	bounds, _ := schedule.DayBounds(date, "UTC")
	old := schedule.Interval{Start: bounds.Start.Add(18*time.Hour + 15*time.Minute + 20*time.Second), End: bounds.Start.Add(19*time.Hour + 15*time.Minute + 20*time.Second)}
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "UTC", []schedule.Interval{old}); err != nil {
		t.Fatal(err)
	}
	saved := readDays(t, s, c.ID, o.ID)
	canonical, err := CanonicalAvailabilityDays(saved)
	if err != nil {
		t.Fatal(err)
	}
	cell := schedule.Interval{Start: bounds.Start.Add(21 * time.Hour), End: bounds.Start.Add(21*time.Hour + 30*time.Minute)}
	wants, blocked := schedule.PaintIntervals(canonical[0].Windows, canonical[0].Blocked, cell, "want")
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, date, "UTC", wants, blocked); err != nil {
		t.Fatalf("unrelated paint rejected legacy seconds: %v", err)
	}
	got := readDays(t, s, c.ID, o.ID)
	if len(got) != 1 || len(got[0].Windows) != 2 || !reflect.DeepEqual(got[0].Windows[0], old) {
		t.Fatalf("legacy boundary changed %+v", got)
	}
	original := got
	invalid := schedule.Interval{Start: cell.Start.Add(time.Nanosecond), End: cell.End}
	if err := s.SaveAvailabilitySlots(c.ID, m.ID, date, "UTC", []schedule.Interval{invalid}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nanoseconds accepted %v", err)
	}
	if got := readDays(t, s, c.ID, o.ID); !reflect.DeepEqual(got, original) {
		t.Fatalf("invalid nanos changed old data %+v", got)
	}
}
