# Church Calendar: the members area (pilot) and the public `/calendar.json`

The cross-app design is in `../.claude/plans/members-site.md` (and the
public feed in `../.claude/plans/church-website.md`, section "The
calendar's part"); this is the calendar's part. **This app is the pilot**:
the pattern built here (token check, local user, CORS, idempotency) is
copied by the directory and the event planner, so keep it small and clean.
Everything still works with the calendar alone: with User Management not
connected, the `/me` API is off and nothing else changes.

Status: planned 2026-10-16, not built.

Next free migration is `005_…` (001 is frozen; 002 to 004 exist). Anything
stored goes in `005_member_api.sql`, never in an old file.

## What changes, in short

| | Now | After |
|--|-----|-------|
| Public feed | `/calendar.ics` only (`publicICS`) | Plus `GET /calendar.json`, token-free, public bookings and busy blocks, `?public_only=1`, 5-minute cache, no cookie. Step 1, ships on its own |
| Who can call the API | Read tokens `cal_…` (`requireReadToken`), source tokens for pushes | Plus `/api/v1/me/…` taking `Authorization: Bearer umd_…` from User Management, checked with `GET /api/v1/token` |
| Local user for a caller | Session cookie only | The same user row as SSO sign-in (`sso_subject`), found or created, then the normal role rules |
| CORS on `/api/` | `*` for GET (`apiHandler`) | `/api/v1/me/…` allows only the website origin (a setting on the User Management connection), never `*`; no cookies |
| Member actions | Server-rendered pages: `/request`, `/my/bookings`, `/request/{id}/edit`, `/cancel` | The same actions as JSON, sharing the same store calls and rules |
| Writes | Form posts with CSRF | JSON writes with an `Idempotency-Key` (new table in 005) |
| Settings | SSO connection: address, client id, secret | Plus the website origin on the same connection, and "Accepts tokens from" shown read-only (User Management owns it) |

## Steps

### 1. Public feed `GET /calendar.json` (own early step, no User Management needed)
Twin of `publicICS` in `internal/server/api.go`. Can ship before anything else.
- [ ] `publicJSON` handler in `internal/server/api.go` next to `publicICS`: `apiRange` (`from`/`to`, default 90 days, max 400), `calendarFor(r, from, to, store.AccessPublic)`, then the same `apiEventJSON(r, e)` shape as `apiEvents`
- [ ] **Equipment never appears**: do not call `apiEquipment` at all, and leave `Equipment` nil for every entry (a public feed has no token; equipment is never public)
- [ ] Busy blocks (rooms and times only, from `store.EntryFor`) are included as visitors see them; `?public_only=1` drops entries with `Busy` so only public bookings in full remain (the website always sends it)
- [ ] Headers: `Cache-Control: public, max-age=300`, `Content-Type: application/json`; no `Set-Cookie`. Add `/calendar.json` to `isCookieFree` in `routes.go` so no session or CSRF cookie is touched, and keep `Vary` clean
- [ ] Routes in `apiRoutes`: `api("GET /calendar.json", s.publicJSON)` and `api("OPTIONS /calendar.json", …)`, wrapped by `apiHandler` so the existing GET CORS applies
- [ ] Pending/tentative show with their `status` as in `/api/v1/events`; the signup link fields follow the booking's visibility exactly as there (public only)
- [ ] Docs: README feed table, `PLAN.md` read-API section; tick "Church website switched to this calendar's read API" only when the website is wired (not here)

### 2. Migration `005_member_api.sql`
- [ ] `idempotency_keys(user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, key TEXT NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL, body_hash TEXT NOT NULL, status INTEGER NOT NULL DEFAULT 0, response TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, PRIMARY KEY (user_id, key))`. `status` 0 means "in progress"
- [ ] `store`: `ClaimIdempotency`, `FinishIdempotency`, `PruneIdempotency` (older than 24 h; pruned from the existing background loop, e.g. next to `tracker_sync.go`)
- [ ] Settings keys (no table change): `sso_website_origin` (see step 3)
- [ ] `go test ./...` with a test that runs 001 to 005 on a copy holding sample rows

### 3. Connection settings: website origin and "Accepts tokens from"
Both are fields on the existing `sso` connection def in `handlers_connections.go`, so env overrides them as a whole connection like everything else.
- [ ] `connections.Field` `sso_website_origin` (`SSO_WEBSITE_ORIGIN`, URL, optional, hint `https://www.example.org`). Normalized to scheme + host (no path, no trailing slash); refuse `*` and any non-http(s) value. Blank means the `/me` API stays off
- [ ] **Accepts tokens from**: User Management owns this list (its `MEMBERS_PLAN.md`, `apps.accepts_from`) and refuses to delegate to an app that doesn't accept the website, so the calendar keeps no local copy. The connection page shows it **read-only** ("Accepts tokens from: website, set in User Management"), or a link to User Management's Apps page if its answer doesn't carry the list. Same as `../event_planning/MEMBERS_PLAN.md` step 4
- [ ] The save goes through the existing pending-then-test flow in `handlers_sso_pending.go`, so a bad value never breaks working sign-in
- [ ] `Server.MembersAPI()`: true only when `s.SSO().Enabled()`, and the website origin is set. Reloaded in `reloadSSO` like the client

### 4. Check a token with User Management (`internal/sso`)
- [ ] `sso.Client.CheckToken(ctx, token) (*TokenUser, via string, err)`: `GET {internal or public URL}/api/v1/token` using the existing `c.do` with the client's Basic auth (SSO client id and secret) and header `X-Token: umd_…`. Parse `{"user":{sub,username,name,email,role},"via":"website"}`
- [ ] Typed errors: `ErrTokenRefused` for 401 (expired, revoked, wrong audience), anything else (timeout, 5xx, bad JSON, connection refused) is "unavailable"
- [ ] Tiny cache in `internal/server` (map + mutex, keyed by a SHA-256 of the token, never the raw token): a good answer lives up to 60 s and never longer; refusals are not cached; entries pruned on lookup; at most a few thousand entries. Reuse the `grantChecks` single-flight idea from `sso.go` so a burst of calls makes one check
- [ ] Unit tests in `internal/sso/sso_test.go` with `httptest`: headers sent (Basic, `X-Token`), 200 parsed, 401 gives `ErrTokenRefused`, 500 and closed server give "unavailable"

### 5. The `/api/v1/me/…` gate
- [ ] New file `internal/server/api_me.go` with `meRoutes(mux)`, called from `apiRoutes`. Routes are registered **without** `apiHandler` (which sets `Access-Control-Allow-Origin: *`) and use a `meHandler` wrapper instead; the more specific `/api/v1/me/` pattern wins over the `/api/` catch-alls
- [ ] `meHandler`: (a) 404 when `!s.MembersAPI()`; (b) CORS: if `Origin` equals the configured website origin exactly, set `Access-Control-Allow-Origin: <that origin>`, `Vary: Origin`, methods `GET, POST, PUT, DELETE, OPTIONS`, headers `Authorization, Content-Type, Idempotency-Key`, `Max-Age: 600`; any other origin gets no CORS headers and its preflight gets 403; never `*`, never `Allow-Credentials`; (c) `Cache-Control: private, no-store`; (d) never sets a cookie
- [ ] Token step: require `Authorization: Bearer umd_…`; a `cal_` token or no header gets 401 with `WWW-Authenticate`. Call the cached `CheckToken`. `ErrTokenRefused` gives 401; unavailable gives **503** with `Retry-After: 5`, and nothing is accepted from an old cache entry past 60 s; `via` other than `website` gives 403
- [ ] Local user: `s.store.UpsertSSOUser(u.Sub, u.Username, u.Name, store.RoleFromSSO(u.Role))`, exactly what `checkGrant` and the SSO callback do (`sso.go`, `store/sso_users.go`). An inactive local user gets 403. The user goes in the request context (new `meUser(r)`), not the `loadSession` path; add `/api/v1/me/` to `isCookieFree` (it already matches `/api/`) and test no `Set-Cookie`
- [ ] Roles: any role may use `/me` (a scheduler or admin is still a member there); it never grants the scheduler or admin actions, which stay in the server-rendered back office
- [ ] Errors use `apiError` and the same JSON shape as the rest of the API; request bodies limited with `http.MaxBytesReader` (64 KB), JSON decoded with `DisallowUnknownFields`

### 6. Read endpoints
- [ ] `GET /api/v1/me/calendar?from=&to=&room=`: `s.calendarFor(r, from, to, store.AccessPrivate)` (via `store.AccessFor(user)`): public and private in full, locked as busy blocks only. Same `apiEventJSON` shape and `apiRange` limits. **No equipment** in this list (see Questions)
- [ ] `GET /api/v1/me/calendar/{id}`: `s.store.CalendarBooking(id, access)`; 404 for a booking not shown in full, as `apiEvent` does
- [ ] `GET /api/v1/me/rooms`: bookable rooms grouped under their building, from `s.bookableRooms()` and `groupRooms` in `handlers_requests.go` (the same list `/request` shows; a bookable building with no bookable rooms is one loose choice)
- [ ] `GET /api/v1/me/availability?rooms=&starts_on=&…`: reuse the logic of `handleRequestAvailability` (split the core into `s.availability(r.URL.Query()) availability` and have both handlers call it). Busy blocks only, never titles
- [ ] `GET /api/v1/me/bookings`: the caller's requests and bookings, upcoming first with status, from `s.store.RequestsBy(user.ID)`, using the grouping in `handleMyBookings` (`handlers_my.go`; extract the list-building into `s.myBookings(user)` shared by the page and the API). Phones and other people's data never appear
- [ ] `GET /api/v1/me/bookings/{id}`: one of the caller's own (403/404 for anyone else's, like `myRequest`); includes `can_change` from `canChange`/`takenOver`, and a `taken_over` flag with a link to the event when a planner took it
- [ ] Equipment on these reads: `GET /api/v1/me/equipment?rooms=` lists what is offered with the chosen rooms (`store.OfferedWith`) with what is left at those times (`EquipmentAskedFor` as in the availability handler). Own bookings carry their own lines (`RequestEquipment`, `BookingEquipment`), shortage flag included. Equipment is only here because members can ask for it on `/request` today; nobody else's equipment is ever shown

### 7. Write endpoints (share the existing handlers' rules, don't copy them)
Refactor first so the page handlers and the API call the same code; the pages must behave exactly as before.
- [ ] Split `readRequest` (`handlers_requests.go`) into reading and checking: `requestInput` (title, rooms, dates, times, count, details, equipment map) and `validateRequest(in, user) (store.Request, rooms, equipment, []string)`. `readRequest` fills `requestInput` from the form; the API fills it from JSON. Same rules: title, dates, 7-15 digit phone only for visitors (not needed for members), `readEquipment` limits (offered with the rooms picked, within `Total`, notes up to 200)
- [ ] `POST /api/v1/me/bookings`: validate, then `s.store.BookRequest(&req, rooms)` (the busy check runs inside its transaction). Free: 201 with the booking (status `confirmed`, the request `approved`, `created_by` the member). Busy: **409** `{"error":"busy","busy":[{room, starts_on, start_time, ends_on, end_time}]}` from `busyErrors`' data (extract a `busyBlocks(busy)` that `busyErrors` also uses); nothing saved. Members are never `pending`. No `needs_approval`
- [ ] `PUT /api/v1/me/bookings/{id}`: the checks of `changeable` (own booking, `canChange`, not taken over) then `s.store.UpdateOwnRequest`; busy gives 409 as above and changes nothing; an edit that only overlaps itself saves; 409 `taken_over` / `already_decided` when it can't be changed
- [ ] `DELETE /api/v1/me/bookings/{id}` (cancel): `changeable` then `s.store.CancelOwnRequest`. For an old request still `waiting` from before the booking change this is the same call; it is the "withdraw" too
- [ ] `POST /api/v1/me/bookings/{id}/withdraw`: only for a request still `waiting`, `s.store.WithdrawRequest`; any other status gives 409. (Members' new requests are never waiting, so this only serves old ones.)
- [ ] The ownership check is one helper used by `myRequest` and the API: `ownedBy(req, user)`; another member gets 403, a missing one 404
- [ ] Equipment on writes: the same `equipment` field as the form, passed through `readEquipment`'s rules; shortages are flagged in the answer (`bookingShortNote`'s data), never refused
- [ ] Nothing here lets a member set visibility (they stay private as `BookRequest` makes them), approve, decline, or touch someone else's or a pushed booking

### 8. `Idempotency-Key` on writes
- [ ] All `POST`, `PUT`, `DELETE` under `/api/v1/me/` require `Idempotency-Key` (8-100 printable characters); missing gives 400
- [ ] Wrapper `s.idempotent(h)`: `ClaimIdempotency(user, key, method, path, sha256(body))` first. New claim: run the handler, record the status and body with `FinishIdempotency`, return it. Same key, same request, finished: replay the saved status and body with `Idempotency-Replayed: true` and **do no work**. Same key but a different method, path or body: 422. Same key still in progress: 409 `in_progress`
- [ ] Only 2xx and 409/422 business answers are saved; a 5xx releases the key so a retry can run
- [ ] Pruned after 24 hours (step 2)

### 9. Tests (`internal/server/members_api_test.go`, `calendar_json_test.go`; `httptest` with a fake User Management server)
- [ ] **Token for another audience refused**: fake UM answers 401 for a token meant for another app; API returns 401 and no local user is created
- [ ] **401 from User Management refused**, including a token that was good a minute ago once the 60 s cache ends; a refusal is not cached
- [ ] **UM unreachable gives 503**: server closed or 500; nothing is accepted, even with a stale cache entry older than 60 s; a good answer inside 60 s may be reused, and the fake sees one call for a burst
- [ ] `via` other than `website` gives 403; `/me` is 404 with SSO off or with no website origin; other routes unchanged
- [ ] **A member never sees locked bookings in full**: `/me/calendar` and `/me/calendar/{id}` give a locked booking as a busy block (rooms and times, no title, description, notes or signup link) and 404 for its detail; private ones are in full; public same as visitors; pending and tentative carry their status
- [ ] **Equipment never appears in `/calendar.json`**: a public booking with equipment lines gives no `equipment` key and no equipment names anywhere in the body; same for `?public_only=1`; private and locked bookings never in full; no `Set-Cookie`; `Cache-Control: public, max-age=300`; `?public_only=1` drops busy blocks; range limits as in `/api/v1/events`
- [ ] **CORS refuses other origins**: the website origin gets exact `Access-Control-Allow-Origin` plus `Vary: Origin`; any other origin, and no `Origin`, get no allow header; preflight from another origin 403; the value is never `*`; no `Allow-Credentials`. `/calendar.json` and `/api/v1/events` keep their existing `*` GET CORS
- [ ] **An idempotent replay of a booking makes one booking**: two identical `POST /me/bookings` with one key give identical answers, one `bookings` row, one `requests` row; same key with a different body gives 422; a missing key gives 400; a 5xx is retried
- [ ] **A busy room is refused**: a booking over a confirmed one, over a visitor's `pending` one, over the room's building (family rule) gives 409 with busy blocks (no titles) and saves nothing; two members racing for one slot make one booking
- [ ] Local user: first call creates the user by `sso_subject` (same row as an SSO sign-in later), role from `RoleFromSSO`, a renamed user is updated not duplicated, no session row and no `Set-Cookie` ever
- [ ] Own bookings: edit and cancel work, another member gets 403, an edit into a busy slot is refused and unchanged, an edit overlapping only itself saves, a taken-over booking is refused, withdraw only works while `waiting`
- [ ] The existing page tests (`requests_test.go`, `owner_test.go`, `pending_test.go`) pass unchanged after the step 7 refactor

### 10. Docs
- [ ] `PLAN.md`: a pointer to this file, the roles table note ("members can use the same actions through `/api/v1/me/…`"), the new feed in the read-API section
- [ ] README: the feed table row for `/calendar.json`; a "Members area" paragraph (needs User Management, the website origin field, no cookies, CORS)
- [ ] `.claude/CLAUDE.md`: the `MEMBERS_PLAN.md:` pointer line
- [ ] Ansible and deploy: nothing new to deploy except the settings; list the website origin in the deploy notes so the user sets it on `/admin/connections`
- [ ] Tick `../.claude/plans/members-site.md` build order item 3 only by the user, when the Calendar section on the website works end to end

## Rules to keep in mind while building
- Visibility is the event planner's: public in full, private for members, locked as busy. A busy block is only rooms and times.
- Equipment is never public: not in `/calendar.json`, `.ics`, busy blocks or anything without a token. On `/me` it appears only for the caller's own bookings and the offered list.
- Phone numbers never leave the calendar.
- No new dependencies; no inline styles; no Node. Secrets are never written in files or docs.
- The user runs all `git commit`s.

## Open questions
- ~~"Accepts tokens from": local list or read-only?~~ Settled 2026-10-16: User Management owns it; the calendar shows it read-only, as the event planner does.
- Equipment on `/me/calendar`: left out for now (members' range shows rooms, times and text only). Own bookings and the offered list do carry equipment. Say if the range should include it for private bookings as `/api/v1/events` does for tokens.
