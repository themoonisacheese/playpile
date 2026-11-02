# Crew scheduling

## The interface

The main screen is a **seven-day timetable**, not a date-by-date questionnaire.
Members paint half-hour cells in one of three modes:

- **Want to play**: positive interest, not an RSVP.
- **Can’t play**: an explicit blocker for those minutes.
- **Erase**: remove both marks; the time becomes unknown.

Blank time is never assumed unavailable. Painting one evening must not mark the
rest of that date as a no. Symbols and accessible labels accompany colour.
Desktop supports dragging; phones select a day and tap slots while retaining
normal scrolling. Keyboard users can focus a cell and paint with Space/Enter.
The timetable scrolls internally, with all hours available. Saved partial-cell
marks retain their exact minutes unless that cell is deliberately repainted.

An explicit save applies only edited days, atomically. Unsaved paint stays
visible if a save fails. Clock settings are secondary, and changing dates or
settings must not silently discard edits. A native form fallback remains usable
without JavaScript. A few readable suggestions follow the timetable; sessions
and RSVPs remain separate below it.

### Seeing crewmates’ choices

The main cell remains your paint target. A separate read-only side button shows
up to four crewmate strips in roster order, with a `+` for a larger roster.
Solid, broken, outlined and half-filled strips distinguish want/cannot/unknown/
partial states. Hover names or open the keyboard/touch-friendly dialog; inspection
never paints, saves, changes the selected mode or discards unsaved work. Escape
and Close return focus to the inspector. Only current crewmates appear, excluding
the viewer; someone who has never answered is still shown as unknown.

Every mark uses absolute instants, projected into the viewer’s timezone. A full
slot is positive/blocked only when the entire cell is covered; mixed or partial
coverage is labelled separately. The saved-times disclosure provides exact dates,
offsets and sub-minute legacy edges and also works without JavaScript. These are
saved choices, not live updates or RSVPs; refresh to see subsequent crew changes.

`internal/web/scheduling_crew.go` reuses the already-authorized crew rows and
canonicalizes once per member, unions adjacent source-day pieces and classifies
cells with binary-search coverage. It adds no database reads, writes, migration,
matching logic or game-list work. Names and exact ranges appear once per roster;
each cell carries one state byte per crewmate and at most four visual strips.
Work scales with the bounded week and current roster, never historical requests.

## Ownership

- `internal/schedule`: pure date/slot generation, interval painting and overlap.
- `internal/store`: migration, bounds, member scope, atomic persistence,
  compatibility and retention.
- `internal/web`: server-generated cells, authenticated/CSRF-protected forms,
  rendering, paint interactions and errors. No second matching algorithm.

Scheduling does not add work or Steam requests to game-list/vote requests.
Recurrence, calendar integrations, chat and notifications remain out of scope.
Future weekly patterns should populate these same dated marks, preserving
per-date exceptions rather than introducing another matching engine.

## Time and matching

The editor covers today through six local dates ahead. Upcoming proposals cover
28 elapsed days so every accepted session is visible and manageable. Each member
uses an explicit IANA zone. The browser may suggest a zone but never silently
changes it. Existing future marks keep their saved zone until explicitly cleared.

Slots are generated from actual civil-day bounds, advancing by 30 elapsed
minutes. Clock-change days can have 46 or 50 cells; half-hour DST shifts can yield
47 or 49. Repeated hours have distinct absolute keys and offset-bearing labels;
nonexistent hours are not invented. Persistence and matching use absolute
instants, not naive wall-clock subtraction. Suggestions carry exact instants
through the proposal form; edited/manual times use strict local-time validation.

A whole-crew suggestion requires continuous positive interest from every current
member for the chosen duration. Explicit blockers take precedence over interest,
including conflicting marks inherited from an older overnight answer. Someone
missing positive coverage is a known blocker if any explicit no prevents the
whole window; otherwise they still need to answer. Brief extra attendees must
not hide longer useful windows for a stable subgroup.

Matching normalizes interval unions and considers starts/end-minus-duration
anchors, extending fixed groups’ common intersections. It is not exhaustive
subset search. Binary-search coverage, participant-keyed containment and
post-limit classification avoid the former all-candidate cross-group scan.
Results are bounded and ranked full crew first; only a few are initially shown.

## Persistence and compatibility

Legacy full-day answers and new partial-day marks are distinguished explicitly:
`availability_days.blocked == ''` means the old reviewed-day representation;
a JSON array means explicit slot blockers, with all other unmarked time unknown.
The positive `windows` field remains compatible. New saves validate wants and
blockers for overlap, local-date bounds, whole-second precision and bounded counts.

The slot batch API accepts at most seven unique dates in one saved zone. Actor
identity comes from the authenticated session, never a submitted user ID. Invalid
batches change nothing, including when a later date is invalid. Untouched game,
vote, ownership, membership, proposal and RSVP data must remain unchanged.
Compatibility handling preserves exact legacy boundaries and overnight tails;
repainting or erasing a displayed tail must not leave a hidden source interval.

Store reads/writes retain a 28-day planning bound. Scheduling records expire;
indexed cleanup runs on scheduling mutations. Idle instances may retain expired
rows until their next mutation. This is not a permanent session archive.

## Sessions and trust boundaries

- Availability never counts as attendance; the proposer also starts pending.
- RSVP is yes/maybe/no, or pending when cleared. Only your own response changes.
- Current creator/admin can cancel before the start. Other crew IDs grant no
  authority. Shared links use the recipient’s own clock.
- Proposals are future, at most 12 hours long, end within 28 days and accept an
  optional 160-character title/game label. At most 64 are retained per crew,
  including cancellations.
- Removal from a crew cascades saved marks, planning preferences and RSVPs;
  creator-owned proposals also disappear. Rejoining restores none of them.
- All scheduling mutations require a real CSRF token and bounded form bodies.
- Back up before deployment. The blocker-column migration preserves old saved
  answers and all unrelated tables. Do not run an older binary against new slot
  data: old code would interpret blank portions as reviewed/unavailable; restore
  the previous database backup along with the previous binary for a full rollback.

## Verification

Keep existing security/RSVP/upgrade tests. Add explicit want/cannot/unknown,
blocker precedence, exact partial-cell preservation, legacy overnight edit/erase,
atomic multi-day failure, half-hour keys, DST gaps/repeats and seven-day limits.
An independent deterministic oracle checks 250 legacy and 250 explicit-blocker
schedules, including conflicts where a blocker must override a positive mark.

Before pushing, run the full race-enabled Go suite, vet, JS syntax and interaction
checks. Use sandboxed Chromium for actual desktop dragging, erasing, keyboard,
mobile day selection/scrolling, failed saves and no-JavaScript operation. Review
the rendered screen at desktop and phone widths, not merely the template source.
An independent final review must cover both tracked and new files.
