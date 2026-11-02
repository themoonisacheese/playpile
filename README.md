# playpile

A small self-hosted web app for a group of people who want to decide, together, which game to
play next.

Everyone signs in with Steam. A crew (group) gets one shared list of games: people add games from
the Steam store, vote them up or down, mark which ones they already own, and leave a note. The page
keeps three numbers current so the group can see the whole picture at a glance:

| number | meaning |
| --- | --- |
| **the whole list** | what every game on the list costs, one copy each |
| **for you** | what *you* would still have to spend, i.e. minus what you own |
| **for everyone** | what the group spends together if everybody buys what they are missing |
| **playable now** | games every member already owns (or that are free) |

Each row also shows the price (with discounts), the Metascore, the Steam review summary and how
many of the crew own it. Permission lives in the server's database: membership, roles
(owner / admin / member) and invite links with optional expiry and use limits.

Written in Go, one static binary, SQLite, server-rendered HTML with a little vanilla JavaScript.
No build step, no CDN, no external services beyond Steam.

---

## Quick start (local)

```sh
go build -o playpile ./cmd/playpile

PLAYPILE_DEV_LOGIN=1 \
PLAYPILE_BASE_URL=http://localhost:8080 \
PLAYPILE_DATA_DIR=./data \
./playpile
```

Open <http://localhost:8080>. With `PLAYPILE_DEV_LOGIN=1` the landing page offers a passwordless
local login (`?name=Ada&steamid=dev-ada`), which is how you test without a Steam account or a
public URL. **Never enable it on a deployed instance** — it lets anyone become anyone.

To use real Steam logins, leave `PLAYPILE_DEV_LOGIN` at `0` and set `PLAYPILE_BASE_URL` to the URL
people actually reach — Steam's OpenID realm and the callback URL are built from it.

## Configuration

Everything is environment variables; there is no config file.

| variable | default | meaning |
| --- | --- | --- |
| `PLAYPILE_ADDR` | `:8080` | listen address |
| `PLAYPILE_BASE_URL` | *(derived from the request)* | public origin, e.g. `https://games.example.com`. Set this in production: the Steam login realm and callback are built from it |
| `PLAYPILE_DATA_DIR` | `./data` | directory holding `playpile.db` (created on first run) |
| `PLAYPILE_STEAM_API_KEY` | *(empty)* | [Steam Web API key](https://steamcommunity.com/dev/apikey). Optional but recommended: enables real display names and avatars at login, and the "import my library" button |
| `PLAYPILE_DEFAULT_COUNTRY` | `US` | default store region for new crews; decides which currency prices come in |
| `PLAYPILE_DEFAULT_LANGUAGE` | `english` | store language for names and descriptions |
| `PLAYPILE_DEV_LOGIN` | `0` | passwordless local login. Development only |

Two more exist for tests and local fixtures: `PLAYPILE_STEAM_STORE_BASE` and
`PLAYPILE_STEAM_API_BASE` point the Steam client at a fake server.

## Deploying

The binary is static (`CGO_ENABLED=0`), so deployment is: copy the file, give it a data directory,
run it behind a TLS-terminating reverse proxy.

```sh
make build                                    # -> ./playpile
install -m755 playpile /usr/local/bin/playpile
install -d -o playpile -g playpile /var/lib/playpile
install -d /etc/playpile
install -m640 deploy/playpile.env.example /etc/playpile/playpile.env   # then edit it
install -m644 deploy/playpile.service /etc/systemd/system/playpile.service
systemctl daemon-reload && systemctl enable --now playpile
```

The unit file in `deploy/` runs it as an unprivileged user with a hardened sandbox (read-only
filesystem except the data directory, no new privileges, and so on).

Reverse proxy — anything that terminates TLS will do:

```caddy
games.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

```nginx
location / {
	proxy_pass http://127.0.0.1:8080;
	proxy_set_header Host $host;
	proxy_set_header X-Forwarded-Proto $scheme;
	proxy_set_header X-Forwarded-Host $host;
}
```

Docker works too:

```sh
docker build -t playpile .
docker run -d -p 8080:8080 -v playpile-data:/data \
  -e PLAYPILE_BASE_URL=https://games.example.com playpile
```

**Backups** are one file: `playpile.db` in the data directory (plus its `-wal`/`-shm` siblings while
running). It is a normal SQLite database, so `sqlite3 playpile.db ".backup backup.db"` is safe on a
live instance.

## How it works

### Accounts and permissions

- Login is Steam OpenID 2.0. The callback is verified server-side (`check_authentication`), and a
  nonce cookie ties the callback to the browser that started the login.
- Sessions are random 32-byte tokens in an `HttpOnly`, `SameSite=Lax` cookie, stored in SQLite with
  a per-session CSRF token. Form posts carry the token; JSON posts are exempt because browsers
  cannot send `application/json` cross-site without a CORS preflight.
- Every crew has memberships with a role. Rank is `owner > admin > member`:
  - **member** — add games, vote, mark ownership, write notes, move games to *played*.
  - **admin** — plus: manage invites, change roles (never above their own), remove members, remove
    games, edit crew settings.
  - **owner** — plus: delete the crew. There is exactly one owner, the creator; the owner cannot be
    removed or demoted, so hand over by promoting somebody and stepping down (which requires
    another admin to exist).
- Invite links (`/join/<token>`) are random, optionally expiring, optionally use-limited, and
  revocable. They can grant at most `admin`. Redeeming while already a member is a no-op.

All of that is enforced in SQL and in the handlers; the browser never decides anything.

### Data

SQLite, three kinds of data: people and permissions (`users`, `sessions`, `crews`, `memberships`,
`invites`), the list itself (`entries`, `votes`, `ownership`), and a cache of Steam metadata
(`game_cache`, `review_cache`).

Ownership is per person, not per crew, and has a source: `manual` (you ticked it) or `library`
(imported from Steam). Unticking only removes the manual mark, so a library import is not silently
destroyed. Importing requires the Web API key and a public Steam profile.

### Prices and metadata

- `store.steampowered.com/api/appdetails` gives name, images, description, developers, Metascore,
  review count and the price for the crew's region.
- `store.steampowered.com/api/storesearch` powers the add-game search box.
- `store.steampowered.com/appreviews/<appid>?json=1` gives the review summary
  (`Very Positive`, 92%, count). Some networks/regions answer this endpoint with an HTML page
  instead of JSON; when that happens the app shows the plain review count from appdetails instead
  of breaking.
- Everything is cached in SQLite: metadata and prices for 6 hours, review summaries for 12. Expired
  entries are served immediately and refreshed in the background, so page loads never wait on
  Steam once a game has been seen. The "refresh prices" button on the list page forces a re-fetch.
- Money is integer minor units end to end, formatted per currency (`19,99 zł`, `$19.99`). Games
  priced in a currency other than the crew's are excluded from the totals and called out, rather
  than silently summed into nonsense.
- Steam rate-limits the store aggressively; requests are serialised with a small gap, and the
  catalog caps concurrent upstream fetches.

### Project layout

```
cmd/playpile/          main: config from env, wiring, graceful shutdown
internal/steam/        Steam store/community HTTP client (retries, rate limit, sentinels)
internal/catalog/      caching facade over the client, background revalidation
internal/store/        SQLite schema, queries, permissions primitives
internal/pile/         view model + the money math (the three totals)
internal/schedule/     pure timezone and availability-window matching
internal/web/          HTTP layer: routes, sessions, CSRF, handlers
internal/web/views/    html/template files
internal/web/static/   app.css and the small app.js enhancement
internal/auth/         Steam OpenID 2.0
internal/money/        currency formatting
deploy/                systemd unit and env template
```

Tests: `go test ./...` — store (permissions, invites, votes, ownership), pile (totals, filters,
sorting), web (a full HTTP flow: login → crew → add → vote → own → invite → permission denials).

## Find a time to play

Open **find a time** from a crew. Choose your timezone, then mark availability
for specific dates over the next seven days. Paint **Want to play**, **Can’t play**, or **Erase**
on half-hour slots. Blank time is unknown—not a no. Drag on desktop; pick a day
and tap slots on your phone. Saving applies your edited days together; it does
not create a weekly routine. Slim bars beside each slot show crewmates’ saved
choices; hover for names, or select the bars for a read-only list. Their marks
use your displayed clock, and partial answers stay distinct from unknown time.

Playpile suggests continuous whole-crew windows, with near matches shown
separately and missing answers distinguished from known blockers. Choose a
minimum of one, two or three hours. Use a suggestion or enter a time to propose
a session; add an optional title/game and share its link in your usual chat.
Upcoming sessions are visible up to 28 days ahead.
Everyone RSVPs **yes / maybe / no** separately, including the person proposing.
Only the creator or a crew admin can cancel, before the session starts.

Times use an explicit IANA timezone (for example `Europe/Paris`). The browser
can suggest yours, but never silently switches it. Clear saved future dates
before changing your availability timezone. Invalid or ambiguous clock-change
times are rejected rather than guessed. Session links use each recipient’s
clock. All forms work without JavaScript.

This is a lightweight planning tool, not a calendar archive: ended sessions and
expired availability are cleaned up on subsequent scheduling writes. Weekly
patterns and integrations can be added later on top of dated availability.
See [scheduling design](docs/scheduling.md) for semantics, limits and tests.

## Deliberate limits

- One instance, one database, no federation. A crew's data is yours; there is no sharing between
  instances.
- No notifications, calendar integrations or recurring scheduling rules. Crew scheduling is
  deliberately limited to dated availability and lightweight session proposals.
- Prices are Steam's *current* price for the chosen region, not a historical low. There is no sale
  alerting.
- Metacritic data comes from Steam's store API. Metacritic has no public API, so games Steam has no
  Metascore for (most indies, some newer releases) simply show nothing.
- Free games count as "playable now", but ownership of them is still whatever people ticked.

## Not affiliated with Valve

This is an unofficial tool. Store data, prices and review summaries come from Steam's public
endpoints and remain Valve's. Game names, images and trademarks belong to their owners.

## Non-Steam games

Use **+ non-Steam game** beside the search box to add Minecraft or any other
non-Steam title. Only its name is required. Optionally supply a description,
website/store link, cover image URL, per-person price and Metascore. Blank price
means unknown; zero means free. Prices use the crew's store-region currency and
are maintained manually. When the crew changes region, old manual prices are
not converted or silently relabelled: edit them in the new currency before
they count toward totals.

Manual games share voting, hard-no disqualification, ownership, status and cost
totals with Steam games, but never make Steam requests. Their creator and crew
admins can edit details from the list or detail page. Ownership applies to that
manual entry only; Steam library imports cannot change it. Ownership pills use
Steam avatars with initials as the fallback.

The first launch after upgrading migrates entries to allow a null Steam app ID
and adds crew-maintained metadata and manual ownership. Existing entry IDs and
votes are preserved. Back up the whole data directory while the service is
stopped before upgrading. To roll back to a pre-manual-games release, restore
both the old binary and that database backup: old binaries cannot read manual
entries.
