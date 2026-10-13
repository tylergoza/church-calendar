# Church Calendar

One place that knows **what's happening where and when** at the church:
every event, rehearsal, meeting and room booking on one calendar, with room
conflicts caught across all the apps. People inside or outside the church
can also ask for a room here.

It works on its own, and joins up with the church's other apps when they're
added: the [event planner](../event_planning) and the
[production planner](../production_planning) push their dates and rooms
here, the [maintenance tracker](../maintenance_tracking) supplies the rooms,
and [User Management](../user_management) signs people in. See `PLAN.md`
for the design and what's built so far.

Built the same way as the other apps:

- **Backend:** Go, one static binary with its templates, JS and CSS embedded
- **Database:** SQLite (one file, pure-Go driver, no CGO)
- **Frontend:** server-rendered HTML + [Stimulus](https://stimulus.hotwired.dev), vendored, with the
  same no-build autoloader. No Node, no bundler. Installable as an app (PWA).

## Quick start

Needs Go 1.27 or newer.

```sh
make dev     # http://127.0.0.1:8120, edits to web/ show on refresh
make test    # go vet and go test
make build   # bin/church-calendar
```

On a new install, **Sign in** goes to `/setup` to create the admin account.
It runs on port 8120, so the maintenance tracker (8080), the production
planner (8090), User Management (8100) and the event planner (8110) can run
alongside it.

## Roles

Each role includes the ones before it.

| Can | Member | Scheduler | Admin |
|-----|:------:|:---------:|:-----:|
| See private bookings in full (not locked ones), ask for rooms | ✓ | ✓ | ✓ |
| Make and edit hand-made bookings, answer room requests, manage rooms, see and resolve conflicts | | ✓ | ✓ |
| Users, API tokens, connections, settings and backups | | | ✓ |

Visitors (not signed in) see the public calendar.

## Room requests

Anyone signed in can **Ask for a room** at `/request`; each room shows
whether it's free as they pick times. Visitors can too once an admin ticks
**Let visitors ask for rooms** on **Settings** (off by default; a hidden
honeypot field and a limit of 5 requests an hour per address keep spam
down). Schedulers answer on **Requests**: approving books the rooms (source
"request"), turning down takes a note. A member's request for rooms that
don't need approval, when they're free, is booked straight away. There's
no email yet: people get a `/request/{id}?code=…` link to check back on.

## Reading the calendar from elsewhere

- `/calendar.ics` is the public feed (no token): public bookings, and busy
  times for the rest. Anyone can subscribe from the calendar page.
- The read API is the event planner's, plus `source`, so the church website
  only changes the address and token: `GET /api/v1/events`,
  `/api/v1/events/{id}` and `/api/v1/events.ics` (which also takes
  `?token=`), with `from`, `to` and `room`. An admin makes read tokens
  (`cal_…`) on **API tokens**; one with "Can see private events" also gets
  private bookings in full. Locked bookings are only ever busy.
- `/calendar/by-source/{source}/{id}` sends the apps' old calendar links to
  the booking they pushed.

## Apps sending their events

The event planner and the production planner send their dates here (the
calendar never fetches them). An admin makes an **app token** for each on
**API tokens** and pastes it, with this calendar's address, into the
calendar section of that app's own **Connections** page; that page's
send-everything button brings over what the app already has. **Connected
apps** (`/admin/apps`) shows each app's tokens, when it last sent, and how
many bookings it has here.

With an app token (`Authorization: Bearer cal_…`), under `/api/v1`:

| Endpoint | |
|---|---|
| `PUT /bookings/{source}/{source_id}` | Create or replace that booking (JSON as in `PLAN.md`). Always 200 with the booking and any `conflicts`: a clash is a warning, never a refusal. 400 only for dates or times it can't store, with `fields` |
| `DELETE /bookings/{source}/{source_id}` | 204, even if it was already gone |
| `GET /bookings/{source}?updated_since=` | The app's own bookings, to check it's in step |
| `GET /availability?rooms=Hall,Kitchen&from=&to=` | What's booked in those rooms, from any app (also with a read token) |
| `GET /rooms` | The calendar's rooms, with tracker place IDs (also with a read token) |

A token only touches its own app's bookings (403 otherwise). Rooms are
matched by the tracker's place ID, then by name; unknown places are kept
by name but not checked for clashes, and never become rooms. Locked
bookings are stored with no title or description even if one is sent.
`replaces` (from **Make this an event**, shown on hand-made bookings and
approved requests when the event planner is connected) swaps a hand-made
booking for the event's in one go. Other bookings show to an app as busy
unless its token can see private events; its own always show in full.

## Configuration

Set with a flag or an environment variable. Flags go **before** any command.

| Env var | Flag | Default | |
|---|---|---|---|
| `ADDR` | `-addr` | `:8120` | Listen address |
| `DB_PATH` | `-db` | `data/calendar.db` | SQLite file. Created, with migrations applied, on start. `secrets.key` is made next to it. |
| `TZ` | | system | Church time zone: decides what "today" means |
| `TRUST_PROXY=1` | `-trust-proxy` | off | Trust `X-Forwarded-For/Proto` from a reverse proxy. Only when a proxy is the only way in. |
| `DEV=1` | `-dev` | off | Load templates/static from `./web` on disk |

## Connections

Links to the other apps are set by an admin on **Connections**
(`/admin/connections`). Each is optional; when one is off, the calendar
behaves as if that app doesn't exist.

| Connection | Environment override | What it turns on |
|------------|----------------------|------------------|
| Maintenance tracker | `TRACKER_URL`, `TRACKER_TOKEN` | Importing rooms from the tracker's places |
| Event planner | `EVENTS_URL` (address only) | **Make this an event** on bookings and requests |

Single sign-on joins this list when it's built (`PLAN.md` step 7).

The rules:

- **Environment, then settings, then off.** If **any** of a connection's
  variables is set on the server, the **whole** connection comes from the
  server and the page shows it read-only. A URL from one place is never
  mixed with a token from another.
- **Secrets are write-only.** The page shows "Saved, ends …a1b2", never the
  token. A blank token on save keeps the saved one; **Remove** clears it.
  Saved tokens are encrypted (AES-256-GCM) with `secrets.key`, which sits
  next to the database. A downloaded backup can't be read for its tokens
  without that key; restored without it, the page asks for them again.
- **Saving tests** the connection where it can, and saves even if the other
  app isn't running yet.

Handing a connection over from the server to the app:

1. Deploy with the values set on the server. The row says *Set on the server*.
2. Press **Keep in settings**. The row says the settings match, so it's safe
   to remove the values from the server.
3. Remove them from the server and restart. The row says *Set here*. Nothing
   disconnected.

If a bad saved setting is the problem, `church-calendar connection clear
tracker|events` removes it from the command line.

## Commands

```sh
church-calendar create-user alice [--role member|scheduler|admin]  # default member; --admin is --role admin; prompts for a password (10+ characters)
church-calendar reset-password alice          # and turns their account back on
church-calendar connection clear tracker      # forget a connection's saved settings
church-calendar backup /path/backup.db        # safe while the app is running
```

The first user created is always an admin.

## Backups

**Settings → Download backup**, or the `backup` command, writes a
consistent copy of the database while the app runs. To move to another
server, stop the app there, put the file at its `DB_PATH`, copy
`secrets.key` next to it, and start it again.

## License

MIT. See `LICENSE`.
