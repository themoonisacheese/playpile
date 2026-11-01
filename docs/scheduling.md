# Crew scheduling

## Product model

Scheduling answers “when can this crew play?” without turning Playpile into a
calendar application. The foundation is availability on **specific dates**.
Weekly patterns, if added later, should populate the same dated records; they
must not introduce a second matching system or overwrite date-specific edits.

Three concepts stay separate:

1. **Availability:** a member has checked a date and marked playable windows,
   or explicitly said they cannot play. An unanswered date is unknown.
2. **Suggestions:** continuous overlapping windows derived from current crew
   members’ availability. Whole-crew matches are distinguished from near matches.
3. **Session proposals:** a particular time the crew can agree to. Each member
   responds yes, maybe or no. No response means pending, never implicitly yes.

No recurring rules, calendar integrations, chat, background notifications or
automatic booking in this first version.

## Ownership and execution

- `internal/store`: additive SQLite schema, membership-scoped persistence,
  atomic replacement, validity/permission rules, bounded retention.
- `internal/schedule`: pure interval arithmetic and timezone/date validation.
- `internal/web`: authenticated forms, CSRF, presentation, progressive
  enhancement. It must not implement a second overlap algorithm.

Matching runs only on the scheduling page, over a bounded planning window;
normal pile rendering and voting acquire no scheduling work or network
requests. No Steam calls are required by scheduling.

## Time and matching

The availability editor shows 14 dates in the member’s chosen IANA timezone. Upcoming
proposals cover the full accepted 28-day horizon so saved sessions remain visible
and manageable. UTC is the
no-JavaScript fallback; JavaScript only offers the browser timezone, never
silently changes it. A member’s saved timezone remains fixed while future
answers (including overnight spillovers) exist. Clear those dated answers to
switch zones. This avoids reinterpreting old civil dates when the clock changes.

Each date accepts up to eight windows. An explicitly checked date with no
windows means unavailable; clearing a date makes it unknown. Overnight windows
start on the chosen date and explicitly end the next day. Their spillover stays
available even if the next date has an unavailable answer: the saved playable
window takes precedence. Edit the source date to change that window.

Absolute instants are stored for arithmetic; timezone data is embedded for
static deployments. Manual entry rejects nonexistent or repeated wall minutes
at daylight-saving changes rather than guessing an occurrence. Dates can be
23 or 25 hours long. Matching durations are elapsed time, not naive wall-clock
subtraction. Suggestions preserve offset-bearing absolute instants through the
proposal form, so a valid overlap can still be proposed during a repeated local
hour. Editing the local fields restores strict manual parsing.

Matching normalizes each member’s interval unions, then considers anchors at
window starts and at ends minus the requested duration, extending each fixed
participant group’s common intersection. This finds whole-crew intersections
and useful longer subgroup windows without enumerating every subset of the
crew. It is deliberately not an exhaustive subgroup optimizer. Contained
same-group suggestions are removed, full-crew matches rank first, and the page
shows at most 24 suggestions. Near matches require two continuously available
people (a one-person crew can still get whole-crew windows).

Normalized coverage uses binary search. Containment checks are indexed by
participant tuple rather than comparing unrelated groups, and only the final
ranked/limited results receive missing-member classifications. With 14 days ×
two staggered windows per member, an independent local check measured about
3.4 ms for 10 members and 125 ms for 100, versus 138 ms and 17.4 s before this
optimization. These are fixture timings, not service latency guarantees; there
is no crew-member cap and matching is not a constant-time operation.

Every suggestion’s participants can attend the **entire** window. A missing
member is “cannot make the whole window” if any explicitly unavailable portion
blocks it; otherwise they still need to answer part or all of it. A brief third
person joining must not hide a longer playable session for the other two.

## Permissions, lifetime and storage

- A member edits only their own availability and responses. Actor identity
  comes from the authenticated session, never a submitted user ID.
- All scheduling POSTs require a real form CSRF token and have a 32 KiB body cap.
- Only a proposal’s creator or a current crew admin can cancel it. Cross-crew
  IDs grant no authority. RSVP/cancel is allowed before the start time only.
- Availability is not an RSVP. The creator also starts pending. Changes to
  availability do not silently modify proposals or responses.
- Membership removal cascades the member’s availability, timezone preference
  and RSVPs; they do not reappear on rejoining. Proposals created by the removed
  member also disappear, along with their responses.
- Invalid writes leave previous availability intact. Day replacement is atomic.
- Store reads accept a maximum 28-day interval; writes accept today through
  27 dates ahead in the member’s zone. A window is at most 24 elapsed hours.
- Proposals are future, at most 12 hours long, end within 28 days, and accept an
  optional plain-text title up to 160 characters. There are at most 64 retained
  proposals per crew, counting cancellations. Long availability suggestions
  prefill at most 12 hours for a proposal; the visible availability is unchanged.
- Expired availability and ended proposals are pruned opportunistically on
  scheduling writes, using expiry indexes. There is no growing session archive.
  An idle instance may retain expired rows until its next scheduling mutation.
- The migration creates new tables and indexes without rewriting games, votes,
  ownership, memberships or browser sessions. Back up before upgrading as usual.
  Older binaries ignore the additive scheduling tables, but do not prune them.

## Verification

Unit tests cover boundaries, continuous participant identities, unknown versus
unavailable, duration thresholds, midnight and DST. An independent deterministic
randomized oracle checks 250 schedules against discrete availability states,
including full-crew coverage and every returned classification. Store tests
cover migration/reopen, invalid-write atomicity, membership cascades, scoped
proposals/RSVPs, cancellation and retention limits/cleanup. HTTP tests use real
login/crew/forms and test outsiders, forged IDs, CSRF, invalid time input and
no-JavaScript operation.

Before pushing: run `go test -race -count=1 ./...`, `go vet ./...`, JavaScript
syntax checks, a sandboxed desktop/mobile browser walkthrough, and an independent
read-only review of tracked and untracked files. Exact release results belong
in the implementation report rather than an unchecked checklist.
