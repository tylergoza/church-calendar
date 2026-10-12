# Church Calendar: plan

One place that knows **what's happening where and when** at the church: every
event, rehearsal, meeting and room booking on one calendar, with room
conflicts caught across all the apps. People (inside or outside the church)
can also ask for a room here.

Status: planned, nothing built yet (2026-10-11).

## The idea in one paragraph

Each app keeps working on its own. The calendar is useful alone (a church
that only wants a public calendar and room booking). When the event planner
or the production planner is added, it **pushes** its dates and rooms to the
calendar, and the calendar becomes the one public calendar and the one place
room clashes are found. Adding an app later is an import, not a rebuild:
the app sends everything it already has, and the calendar picks up rooms
from the maintenance tracker if there is one.

## Who owns what

| App | Owns | Sends to / reads from the calendar |
|-----|------|------------------------------------|
| Maintenance tracker | Rooms (places) | Calendar reads rooms from its API (optional) |
| **Calendar** | Bookings, room requests, which rooms can be booked, the public calendar, `.ics` feeds, the read API | — |
| Event planner | Events: tasks, helpers, needs, run of show | Pushes each event date with its rooms |
| Production planner | Productions: cast, mics, readiness | Pushes each rehearsal and performance with its rooms |
| User Management | Accounts and per-app roles | Calendar signs in through it (optional) |

The calendar keeps a **thin copy** of each event: times, rooms, title,
public description, visibility, and a link back. It never edits another
app's events. Notes, tasks, helpers, cast and needs never reach it.

**Don't** turn this around and make the calendar where events are created
with the apps reading from it. That makes the calendar a dependency for
everything, and it would need to understand productions and event tasks.

## Running alone, and joining up later

Every link is optional and follows the existing `Configured()` pattern
(`internal/tracker` in the planners): no URL and token set means the feature
is off and the app behaves as if the other app doesn't exist.

| Setup | What works |
|-------|-----------|
| Calendar alone | Rooms typed in by hand, bookings made by hand (with simple repeats), room requests, public calendar, `.ics`, read API, local login |
| + Maintenance tracker | **Import rooms** from the tracker's places; rooms keep the tracker's place ID and cached name, as the planners do |
| + Event planner | Its events arrive by push; the **Send all events to the calendar** button in the calendar section of its **Connections** page backfills what it already has. Its own `/calendar` redirects here |
| + Production planner | Its rehearsals and performances arrive by push, backfilled by the same kind of admin button |
| + User Management | Single sign-on; local login off, as in the other apps |

Going the other way (the calendar was set up first and has hand-made
bookings, then the event planner is added): a booking's **Make this an
event** button opens the event planner with it filled in, and the planner's
push then takes over that booking (see `replaces` below), so nothing is
entered twice and nothing shows up twice.

## Stack and conventions

Same as the siblings (copy from `../event_planning`, the newest):

- Go, one static binary, templates/JS/CSS embedded. SQLite (modernc, no
  CGO), numbered migrations in `internal/store/migrations/`.
- Server-rendered HTML + vendored Stimulus with the no-build autoloader.
  A PWA (manifest + service worker) like the others.
- No new dependencies.
- Port **8120**. Cookies `cal_*`. Tokens start `cal_`.
- SSO client ID `calendar`; secret `UM_CALENDAR_SECRET`, Keychain service
  `um-calendar`.
- Deployed by Ansible to the tracker's droplet as `calendar.<dns_zone>`,
  service user `calendar`, data in `/var/lib/church-calendar`, Caddy site
  `/etc/caddy/sites/church-calendar.caddy`, binary
  `/usr/local/bin/church-calendar`.

## Roles

| Role | Can |
|------|-----|
| `member` | See private bookings in full (not locked ones), ask for rooms |
| `scheduler` | Also make and edit hand-made bookings, approve or turn down requests, manage rooms, see and resolve conflicts |
| `admin` | Also users (local login), API tokens, connected apps, settings |

Visitors (not signed in) see the public calendar and can send a room
request if the admin has turned public requests on.

## Data model (first migration)

```sql
-- A room or space that can be booked.
CREATE TABLE rooms (
    id               INTEGER PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    tracker_place_id INTEGER UNIQUE,          -- NULL when typed in by hand
    tracker_path     TEXT NOT NULL DEFAULT '', -- cached "Main building › Lifecenter"
    capacity         INTEGER,
    bookable         INTEGER NOT NULL DEFAULT 1,  -- shown on the request form
    needs_approval   INTEGER NOT NULL DEFAULT 1,  -- requests wait for a scheduler
    notes            TEXT NOT NULL DEFAULT '',
    archived         INTEGER NOT NULL DEFAULT 0
);

-- One block of time on the calendar.
CREATE TABLE bookings (
    id           INTEGER PRIMARY KEY,
    source       TEXT NOT NULL,   -- 'calendar', 'request', 'events', 'productions'
    source_id    TEXT,            -- the other app's ID, e.g. '12' or 'date-88'; NULL for calendar/request
    series_id    INTEGER REFERENCES series(id) ON DELETE SET NULL,  -- hand-made repeats only
    title        TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL DEFAULT '',
    starts_on    TEXT NOT NULL,             -- YYYY-MM-DD
    start_time   TEXT NOT NULL DEFAULT '',  -- HH:MM, '' = all day
    ends_on      TEXT NOT NULL DEFAULT '',  -- '' = same day
    end_time     TEXT NOT NULL DEFAULT '',
    visibility   TEXT NOT NULL DEFAULT 'private'
                 CHECK (visibility IN ('public', 'private', 'locked')),
    description  TEXT NOT NULL DEFAULT '',  -- the public description only
    url          TEXT NOT NULL DEFAULT '',  -- back to the owning app's page
    status       TEXT NOT NULL DEFAULT 'confirmed'
                 CHECK (status IN ('confirmed', 'tentative', 'cancelled')),
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (source, source_id)
);
CREATE INDEX bookings_dates ON bookings(starts_on, ends_on);

CREATE TABLE booking_rooms (
    booking_id INTEGER NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    room_id    INTEGER REFERENCES rooms(id) ON DELETE SET NULL,  -- NULL: a place we don't know
    room_name  TEXT NOT NULL,   -- as sent, so unknown places still show
    PRIMARY KEY (booking_id, room_name)
);
CREATE INDEX booking_rooms_room ON booking_rooms(room_id);

-- Someone asking for a room. Approving it makes a booking (source 'request').
CREATE TABLE requests (
    id            INTEGER PRIMARY KEY,
    name          TEXT NOT NULL,
    contact       TEXT NOT NULL,             -- email or phone
    user_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
    title         TEXT NOT NULL,
    starts_on     TEXT NOT NULL, start_time TEXT NOT NULL DEFAULT '',
    ends_on       TEXT NOT NULL DEFAULT '', end_time TEXT NOT NULL DEFAULT '',
    expected_count INTEGER,
    details       TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'waiting'
                  CHECK (status IN ('waiting', 'approved', 'declined', 'withdrawn')),
    answer        TEXT NOT NULL DEFAULT '',  -- note back to the person
    booking_id    INTEGER REFERENCES bookings(id) ON DELETE SET NULL,
    decided_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE request_rooms (request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
                            room_id INTEGER NOT NULL REFERENCES rooms(id),
                            PRIMARY KEY (request_id, room_id));

-- API tokens. kind 'read' = read API; kind 'source' = an app that pushes.
CREATE TABLE tokens (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,             -- "Event planner", "Church website"
    kind        TEXT NOT NULL CHECK (kind IN ('read', 'source')),
    source      TEXT,                      -- 'events', 'productions' for kind 'source'
    see_private INTEGER NOT NULL DEFAULT 0,
    hash        TEXT NOT NULL UNIQUE,      -- sha256, as in the event planner
    prefix      TEXT NOT NULL,
    last_used   TEXT,
    revoked_at  TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
```

Plus `users`, `sessions`, `settings`, and `series` (hand-made repeats),
copied from the event planner. Conflicts are **worked out when read**
(overlapping times in the same `room_id`, not cancelled), not stored, so
they can't get out of date. A small `conflict_ok` table (pairs of booking
IDs a scheduler marked "that's fine") hides ones that are known and agreed.

## Visibility

The same rules the event planner uses today, so moving its calendar here
changes nothing for visitors:

| | public | private | locked |
|--|--------|---------|--------|
| Visitors, read tokens without private access | Full | Busy | Busy |
| Read tokens with private access, members | Full | Full | Busy |

"Busy" is only rooms and times ("Lifecenter unavailable · 2:00–4:00 pm").
Apps send no title or description for locked bookings at all, so a
mistake here can't leak them.

## The push API (for apps)

All under `/api/v1/`, `Authorization: Bearer cal_…` with a **source**
token. A source token can only touch bookings with its own `source`.

| Endpoint | Does |
|----------|------|
| `PUT /api/v1/bookings/{source}/{source_id}` | Create or replace that booking. Idempotent: sending the same thing twice is fine |
| `DELETE /api/v1/bookings/{source}/{source_id}` | Remove it. 204 even if it was already gone |
| `GET /api/v1/availability?rooms=Lifecenter,Kitchen&from=…&to=…` | Busy blocks in those rooms (any source), for warnings while someone picks rooms |
| `GET /api/v1/rooms` | Bookable rooms, with tracker place IDs |
| `GET /api/v1/bookings/{source}?updated_since=…` | That source's bookings, so an app can check it's in step |

`PUT` body:

```json
{
  "title": "Fall Fellowship Dinner",
  "kind": "Fellowship dinner",
  "starts_on": "2026-11-07", "start_time": "17:30",
  "ends_on": "", "end_time": "20:00",
  "rooms": [{"place_id": 41, "name": "Lifecenter"}, {"name": "Pavilion"}],
  "visibility": "public",
  "description": "Potluck dinner for everyone.",
  "url": "https://events.example.org/events/12",
  "status": "confirmed",
  "replaces": 305
}
```

- **Rooms are matched** by `place_id` to `rooms.tracker_place_id`, then by
  name (case-insensitive). No match: stored by name only, shown, but not
  checked for conflicts. The calendar never creates rooms from a push.
- **The answer is always 200** with the booking and any `conflicts`
  (overlapping bookings in the same rooms, shown busy unless the token can
  see them). The calendar does **not** refuse an app's booking: the event
  already exists in that app, and refusing would leave the two out of step
  and jam the retry queue. The app shows the conflicts as a warning; the
  calendar lists them for schedulers.
- `replaces` (optional): the ID of a `calendar` or `request` booking this
  one takes over. The calendar deletes that booking in the same
  transaction and moves its request link. Used by **Make this an event**.
- 400 for bad dates or times, with a message per field. 403 for the wrong
  source.

## The read API (for websites and other tools)

Same shape as the event planner's `/api/v1/events` so the church website
only has to change the address and token:

| Endpoint | Returns |
|----------|---------|
| `GET /api/v1/events?from=&to=&room=` | `{"events": [...]}` with the event planner's fields plus `source` |
| `GET /api/v1/events/{id}` | One booking; 404 when the token can't see it in full |
| `GET /api/v1/events.ics` | iCalendar, `?token=` allowed |
| `GET /calendar.ics` | Public feed, no token |

Same limits as today: `from` defaults to today, `to` to 90 days on, at most
400 days. CORS open for GET.

## Pages

| Page | What |
|------|------|
| `/` and `/calendar` | Month grid on wide screens, agenda on phones; filter by room and by source ("Events", "Productions", "Other"). Same look as the event planner's calendar, which it's moved from |
| `/calendar/{id}` | One booking. Links to the owning app for signed-in people |
| `/calendar/by-source/{source}/{source_id}` | Redirects to that booking, so the apps can send old links here |
| `/rooms`, `/rooms/{id}` | Rooms and a week view of each. Schedulers: add, edit, archive; **Import from tracker** |
| `/request` | Ask for a room: pick rooms and times, see which are free as you go |
| `/requests` | Schedulers: waiting, approved, declined. Approve (makes the booking), decline with a note, **Make this an event** |
| `/bookings/new`, `/bookings/{id}/edit` | Hand-made bookings with simple repeats. Bookings from another app are read-only here, with a link to edit them there |
| `/conflicts` | Clashes from today on, grouped by room, with "That's fine" |
| `/admin/tokens`, `/admin/apps`, `/admin/users`, `/admin/settings`, `/admin/connections` | As in the event planner. **Connected apps** shows each source, its last push and how many bookings it has, with a reminder that the backfill button is on that app's own **Connections** page (the calendar can't pull, the apps push) |

**Make this an event** builds
`{EVENTS_URL}/events/new?from_calendar={booking id}&title=…&starts_on=…&rooms=…`
and only shows when the event planner's address is set (Connections). The event planner fills the form
and sends `replaces` on its first push.

## Settings

Server-only (environment):

| Var | Default | Use |
|-----|---------|-----|
| `ADDR` | `:8120` | |
| `DB_PATH` | `data/calendar.db` | |
| `DEV`, `TRUST_PROXY`, `TZ` | | As the siblings |

Connections are set by an admin on `/admin/connections`, with the
environment overriding them, as designed in
`../event_planning/CONNECTIONS_PLAN.md` (whole-connection override,
encrypted secrets in `secrets.key`, **Keep in settings**, SSO only goes live
after **Test sign-in**):

| Connection | Env override | Use |
|------------|--------------|-----|
| Single sign-on | `SSO_URL`, `SSO_INTERNAL_URL`, `SSO_CLIENT_ID`, `SSO_CLIENT_SECRET`, `BASE_URL` | Off = local login |
| Tracker | `TRACKER_URL`, `TRACKER_TOKEN` | Room import; off = rooms by hand only |
| Event planner | `EVENTS_URL` (address only, no token) | Turns on **Make this an event** |

## Things not to do (yet)

- Email or text notifications for requests. People see the status at
  `/request/{id}` (a link with a random code they're given). Add email when
  there's an SMTP setup the other apps can share.
- Two-way sync with Google/Outlook calendars. The `.ics` feed covers
  reading.
- Booking equipment. That's the planners' "needs" and the tracker.

## Steps

Each step is a working app; stop after any of them.

### 1. Skeleton
- [ ] `go mod init`, copy layout, CSRF, sessions, local login, `/setup`, rate limiter, PWA bits and the Stimulus autoloader from `../event_planning`
- [ ] Migration `001_initial.sql` with the tables above
- [ ] Roles `member`, `scheduler`, `admin`; `requireScheduler` / `requireAdmin`
- [ ] `Makefile` (`dev`, `test`, `build`), `.gitignore`, `README.md`, `LICENSE`
- [ ] Connections plumbing copied from the event planner once it's built (`internal/secrets`, the connection type, `/admin/connections`, `calendar connection clear …`); until then, build connections straight into this shape rather than env-only
- [ ] `go vet ./... && go test ./...` pass

### 2. Rooms and hand-made bookings
- [ ] Rooms pages: add, edit, archive, bookable, needs approval
- [ ] Bookings: one-off and all-day, multi-day, rooms, visibility
- [ ] Simple repeats (copy `internal/recur` from the event planner), each date a real booking, edit this one / this and later
- [ ] Conflict query and `/conflicts`, "That's fine"
- [ ] Tests: overlap rules (all-day vs timed, multi-day, cancelled ignored, unknown rooms ignored)

### 3. Public calendar and feeds
- [ ] Move the event planner's calendar templates, CSS and controllers here (month grid + agenda), add the source filter
- [ ] `/calendar/{id}` with the visibility rules
- [ ] `/calendar/by-source/{source}/{source_id}` redirects to that booking's page (the apps' old `/calendar/{id}` links land here)
- [ ] `/calendar.ics`, 5-minute cache
- [ ] Read API `/api/v1/events…` with the event planner's JSON shape plus `source`
- [ ] Read tokens at `/admin/tokens`
- [ ] Tests: visibility table above, one test per cell

### 4. Room requests
- [ ] `/request` form with live availability (Stimulus controller hitting a same-origin JSON endpoint)
- [ ] Public requests setting (off by default); honeypot field and per-IP rate limit
- [ ] `/request/{id}?code=…` status page for the person who asked
- [ ] `/requests` for schedulers: approve (makes a `request` booking), decline with a note
- [ ] Rooms with `needs_approval` off book straight away for members
- [ ] Tests: approve makes exactly one booking; double-approve is refused

### 5. Push API for apps
- [ ] Source tokens (kind `source`, tied to one source)
- [ ] `PUT` / `DELETE /api/v1/bookings/{source}/{source_id}`, room matching, always-200 with `conflicts`
- [ ] `replaces` handling in one transaction
- [ ] `GET /api/v1/availability`, `/api/v1/rooms`, `/api/v1/bookings/{source}`
- [ ] **Connected apps** admin page
- [ ] Tests: idempotent PUT, wrong source 403, `replaces`, locked bookings stored without title even if one is sent

### 6. Tracker import
- [ ] `internal/tracker` client (copy from the event planner; only places) and the **Tracker** connection section
- [ ] **Import from tracker**: pick places to add as rooms; re-running updates names/paths and never duplicates
- [ ] Bookings stored by name that now match an imported room get linked (`room_id` filled in)

### 7. Single sign-on
- [ ] Copy `internal/sso` from the event planner; `RoleFromSSO` maps `member`/`scheduler`/`admin`
- [ ] **Single sign-on** connection section with pending values and **Test sign-in**
- [ ] Register in User Management (see `../user_management/CALENDAR_PLAN.md`)

### 8. Deploy
- [ ] `deploy/ansible` copied from the event planner: service `church-calendar`, user `calendar`, `/var/lib/church-calendar`, Caddy site file, `calendar.<dns_zone>`
- [ ] Main Caddyfile text identical to the other repos' `deploy.yml`
- [ ] Connection variables optional in `vars.yml` (blank = manage in the app). When used, secrets come only from the Keychain helpers (`with-um` gets `UM_CALENDAR_SECRET`; a `with-cal` helper for `CAL_TRACKER_TOKEN`)
- [ ] Backups include `secrets.key`
- [ ] Backups of `/var/lib/church-calendar` the same way as the other apps

### 9. Join the other apps
- [ ] Event planner: `../event_planning/CALENDAR_PLAN.md`
- [ ] Production planner: `../production_planning/CALENDAR_PLAN.md`
- [ ] Maintenance tracker (optional): `../maintenance_tracking/CALENDAR_PLAN.md`
- [ ] Church website switched to this calendar's read API
- [ ] After a few weeks with no problems: event planner's own public calendar left only for when it runs without the calendar
