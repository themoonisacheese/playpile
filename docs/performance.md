# Click latency investigation

Baseline: `9f0e21d` (0.2.2). Production had 20 entries, 20 game-cache rows,
zero review-cache rows; page renders in the journal took 2.9–12.3 seconds.
Steam's review endpoint returned HTML. Each list rebuild synchronously retried
all missing reviews, including after a vote or status change.

## Controlled comparison

Evaluator: `TestClickLatency` in `internal/web/performance_test.go`.
Run: `go test ./internal/web -run '^TestClickLatency$' -count=1 -v`.
Same host, fixtures and evaluator; cached game metadata, reviews fail after
15 ms, three GET/vote pairs at each size. These are local synthetic timings,
not claims about end-to-end production latency.

| Games | Baseline GET | Cache-only GET (confirmation) | Baseline vote | Cache-only vote (confirmation) |
|---|---|---|---|---|
| 1 | 18.1 ms | 2.9–3.1 ms | 17.6–18.2 ms | 2.5–3.0 ms |
| 20 | 87–91 ms | 15–22 ms | 82–83 ms | 9–10 ms |

The initial candidate run gave 14–18 ms GET and 9–11 ms vote at 20 games.
The confirmation supports the preselected >=5x vote improvement criterion.
Raw run logs are retained in `/tmp/playpile-perf/` on the development host.

## Boundaries and correctness

- Display construction reads SQLite only. Add validation and explicit refresh
  remain synchronous; adding an uncached game can still wait for Steam.
- Missing/stale display data refreshes opportunistically in the background,
  at most two tasks, at most 128 retry keys, one-minute completion cooldown.
  Expired keys are pruned; saturated work is deferred to a later view.
- Existing stale metadata stays usable on refresh failure. Cold data may be
  absent until a subsequent view; unavailable reviews are not fabricated.
- Rendering remains O(games * members + games log games) CPU work and local
  database reads. It is not O(1), but external latency no longer multiplies
  the number of games on every click.
- Catalog tests block Steam indefinitely to prove display calls return, verify
  worker/bookkeeping caps and cooldowns, and preserve stale data on failure.
- Chromium verification against real Steam: dropdown add, vote/undo/vote,
  hard-no and ownership all succeeded. Vote requests now use URL-encoded
  bodies matching server form parsing, fixing browser-only CSRF failures.
