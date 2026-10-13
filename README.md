# Church Calendar

One place that knows **what's happening where and when** at the church:
every event, rehearsal, meeting and room booking on one calendar, with room
conflicts caught across all the apps. People inside or outside the church
can also ask for a room here.

It works on its own. See `PLAN.md` for the design and what's built so far.

Built the same way as the other apps:

- **Backend:** Go, one static binary with its templates, JS and CSS embedded
- **Database:** SQLite (one file, pure-Go driver, no CGO)
- **Frontend:** server-rendered HTML + [Stimulus](https://stimulus.hotwired.dev), vendored, with the
  same no-build autoloader. No Node, no bundler. Installable as an app (PWA).

## Works alone, or with the other church apps

The calendar runs on its own, with its own users, rooms and bookings. You
can use just this one app. Connecting another church app is optional and
adds features. Until a connection is set up (on `/admin/connections` or
with environment variables) it is off, and the calendar acts as if that
app doesn't exist. Each app keeps its own data. Here, another app's things
are kept as a thin copy or an ID plus a cached name.

| Connected app | What it adds | What this calendar keeps |
|---------------|--------------|--------------------------|
| Nothing (alone) | Rooms and bookings made by hand, [room requests](#room-requests), the public calendar, `.ics` feeds, the [read API](#reading-the-calendar-from-elsewhere), local login | Its own users, rooms and bookings |
| Maintenance tracker | **Import from tracker** for rooms. A hand-made room with the same name is taken over | The tracker's place ID and a cached name or path |
| Event planner | Its events arrive by push, and **Make this an event** appears on hand-made bookings and approved requests. Its `/calendar` redirects here | A thin copy of each event: times, rooms, title, public description, visibility, link back. Never notes, tasks, helpers or needs |
| Production planner | Its rehearsals and performances arrive by push | The same thin copy. Never cast, mics or readiness |
| [User Management](#single-sign-on) | Sign in with a church account, and roles. Local login is the break-glass | A local user row linked to the User Management ID, so bookings stay |

This calendar owns bookings, room requests, which rooms can be booked,
the public calendar, the `.ics` feeds and the read API. Pushes from apps
are never refused for conflicts: the calendar answers with a warning.

Both sides of each connection are built. Setting them up happens when the
calendar is deployed: tokens, registering the calendar in User Management,
and each planner's **Send all** button to fill in what it already has (see
[Deploying](#deploying)).

Setup is in [Connections](#connections),
[Apps sending their events](#apps-sending-their-events) and
[Single sign-on](#single-sign-on).

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

With single sign-on the role comes from User Management: `admin`,
`scheduler` or `member` (anything else counts as member). Local login: the
Users page has a role select, and `create-user` takes `--role`.

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
| Single sign-on | `SSO_URL`, `SSO_INTERNAL_URL`, `SSO_CLIENT_ID`, `SSO_CLIENT_SECRET`, `BASE_URL` | Signing in through User Management (see [Single sign-on](#single-sign-on)); off = local login |
| Maintenance tracker | `TRACKER_URL`, `TRACKER_TOKEN` | Importing rooms from the tracker's places |
| Event planner | `EVENTS_URL` (address only) | **Make this an event** on bookings and requests |

**Importing rooms.** With the tracker connected, schedulers get **Import
from tracker** on `/rooms`. Pick the buildings, rooms and areas people
book; each becomes a room named as the tracker labels it ("104 – Nursery").
A room typed in by hand with the same name is taken over, not duplicated.
Every import also brings imported rooms' names and paths up to date and
archives rooms whose place was deleted in the tracker. Bookings an app sent
by name before the room existed (by its label or bare name) are linked to
it, so they're checked for conflicts; adding or renaming a room by hand
does the same for its name.

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
sso|tracker|events` removes it from the command line.

## Single sign-on

With single sign-on on, people sign in with their church account in User
Management (the `user_management` repo; client ID `calendar`) instead of a
password here:

1. `/login` sends the browser to User Management's `/authorize`
   (authorization code + PKCE). The state and PKCE verifier wait in a
   10-minute `cal_sso` cookie.
2. User Management sends it back to `/auth/callback` with a code. The app
   checks the state, swaps the code for a grant (server to server), and
   adds or updates the person's local user row by their User Management ID.
   An existing local user with the same username is linked, so their
   bookings and requests stay theirs.
3. Every 5 minutes at most, a session's grant is checked again, which picks
   up name and role changes. If User Management says the sign-in has
   ended (signed out, turned off, access removed), the session ends here
   too. If it can't be reached, sessions keep working for an hour past
   their last good check, so restarting it signs no one out.
4. Signing out here also signs out of User Management, and so of the
   other apps within 5 minutes.

**Setting it up on the Connections page.** A wrong setting could lock every
admin out, so saving never switches it on:

1. Admin › Connections › Single sign-on: fill in the User Management
   address, optional internal address, client ID, client secret and this
   app's address, and **Save**. The values are stored as *pending* (the
   secret encrypted) and sign-in doesn't change. A blank secret keeps the
   current one.
2. Press **Test sign-in**. The app checks the client ID and secret with
   User Management server to server, then sends you through User
   Management and back. Only if that works **and** you come back as an
   `admin` of this app does the pending set become live. Otherwise the page
   says why, the old setting stays and the pending values wait. You stay
   signed in here either way. **Discard** drops the pending values.
3. **Turn off single sign-on** (after a confirm) goes back to local
   passwords. Anyone who only ever signed in through User Management has
   none; set one with `church-calendar reset-password USERNAME`.
4. Values set in the environment are shown read-only; **Keep in settings**
   copies them over, as for the other connections.

Break-glass: `church-calendar local-login on` brings the password form
back, and `church-calendar connection clear sso` removes the saved
settings, pending ones included. A broken saved setting falls back to local
login with a banner for admins; broken environment values stop the app at
start.

With single sign-on on, `/setup` is gone, `/account` links to User
Management's account page, and **Users** lists everyone with access from
User Management: admins change roles and suspend people there (adding,
renaming and passwords are for a user admin in User Management).

## Commands

```sh
church-calendar create-user alice [--role member|scheduler|admin]  # default member; --admin is --role admin; prompts for a password (10+ characters)
church-calendar reset-password alice          # and turns their account back on
church-calendar local-login on|off            # break-glass: password sign-in even with single sign-on on
church-calendar connection clear tracker      # forget a connection's saved settings
church-calendar backup /path/backup.db        # safe while the app is running
```

The first user created is always an admin.

## Registering with User Management

Before turning SSO on, register this app from the `user_management` repo.
Secrets live in the macOS Keychain and reach `make` through the `with-*`
helpers in `~/.zshrc`: `with-do` (DigitalOcean token, service
`digitalocean`), `with-um` (the User Management client secrets) and
`with-cal` (this app's tracker token, service `church-calendar`).

`with-um` needs one more line for this app: `UM_CALENDAR_SECRET`, read from
the Keychain service `um-calendar` the same way as the others. `with-cal` is
a small function of the same shape as the other apps' helpers:

```sh
with-cal() {
  CAL_TRACKER_TOKEN=$(security find-generic-password -s church-calendar -w) "$@"
}
```

```sh
# in ../user_management
with-do make register-app APP=calendar                             # dry run: shows what would change
with-do make register-app APP=calendar DRY_RUN=false               # registers; saves the secret in the Keychain as um-calendar
with-do make register-app APP=calendar DRY_RUN=false ROTATE=true   # lost or leaked secret: a new one, saved over the old
```

The SSO client ID is `calendar`. A new secret stops the old one working at
once, so deploy here straight after rotating.

## Deploying

The app is one binary plus one `.db` file, behind [Caddy](https://caddyserver.com)
for HTTPS. `deploy/` has the systemd unit (`deploy/church-calendar.service`)
and a Caddyfile (`deploy/Caddyfile`) for doing it by hand.

### DigitalOcean with Ansible

`deploy/ansible/` works like the other apps', and by default puts the
calendar **on the maintenance tracker's droplet**. It finds the droplet by
the tracker's tag (`maintenance-tracker`) and adds itself alongside, on port
8120, with its own service user (`calendar`), data folder
(`/var/lib/church-calendar`) and Caddy site, at `calendar.<dns_zone>`.
Provision adds the DNS record (pointing at the same droplet) and the service
user; with no tagged droplet it would create one. Deploy runs the tests,
builds the Linux binary here, backs up the database when the binary changes
(keeping 10 sets: the database and `secrets.key` together, as
`pre-deploy-<time>.db` and `.key` in `/var/lib/church-calendar/backups`),
installs, and checks `/healthz`.

Caddy's config is split so the apps can deploy without overwriting each
other: `/etc/caddy/Caddyfile` only imports `/etc/caddy/sites/*.caddy`, and
each app writes its own site there (`church-calendar.caddy` here). If the
droplet still has the Caddyfile from an older tracker deploy, the calendar's
deploy stops and asks you to deploy the updated tracker first, rather than
taking the tracker offline.

For a droplet of its own instead, set `droplet_name` and `do_tag` to
`church-calendar` in `vars.yml`.

```sh
cp deploy/ansible/vars.example.yml deploy/ansible/vars.yml   # set app_domain, dns_zone, sso_url, base_url
with-do with-um make provision         # first time (ends with a deploy, so it needs with-um too)
with-do with-um make deploy            # every update after that
```

On the first deploy the admin user is created before the app starts, so
`/setup` is never exposed. Set `CAL_ADMIN_PASSWORD` to choose its password,
or a random one is printed at the end. With `sso_url` set there's no local
admin to create: people come from User Management.

The connections are optional. Leave each blank in `vars.yml` to manage it in
the app on `/admin/connections`. To set one in `vars.yml` instead: fill in
`tracker_url` and let `with-cal` pass the token from the Keychain
(`church-calendar`, `CAL_TRACKER_TOKEN`: an API token made in the tracker
under API, for example named "Calendar"), or fill in `events_url`, the event
planner's address (no token).

First deploy with connections in `vars.yml`:

1. `with-do with-um with-cal make deploy`.
2. Sign in, open **/admin/connections**, and press **Keep in settings** for each connection set in `vars.yml`. The app saves it (tokens are encrypted with `secrets.key`).
3. Blank those values in `vars.yml` and redeploy. The app keeps its saved settings.

The calendar can't pull events, so to fill it in, press **Send all events to
the calendar** on the event planner's own Connections page once. Pressing it
again is safe.

Backups include `secrets.key`, which the saved tokens need, so restore both
files together. Neither playbook touches `secrets.key`. If it's deleted, the app
makes a new one and the saved tokens have to be entered again.

#### Turning on single sign-on

SSO is the expected setup, since User Management is already live. Set
`sso_url` and `base_url` in `deploy/ansible/vars.yml`, register the app
(above), and deploy with `with-um` so `UM_CALENDAR_SECRET` is set:

```sh
# in deploy/ansible/vars.yml:
#   sso_url: https://accounts.example.org
#   base_url: https://calendar.example.org   # sign-in returns to base_url/auth/callback
with-do with-um make deploy
```

`sso_internal_url` defaults to `http://127.0.0.1:8100` (User Management on
the same droplet). The deploy stops before building if `sso_url` is set
without the secret or `base_url`, so deploy through `with-um` every time
while SSO is on. The secret goes only into the root-only systemd drop-in,
with nothing logged. To turn SSO back off, remove `sso_url` and deploy
again. If User Management is down, use the `local-login on` break-glass
(see [Single sign-on](#single-sign-on)).

With a hardware SSH key, use a deploy key as described in the tracker's
README (`ssh_private_key_file` in `vars.yml`; the same key works for all the apps).

### Moving between hosts

**Settings → Download backup** (or the `backup` command) writes a consistent
copy of the database while the app runs. Stop the app on the new host, put the
file at its database path (`/var/lib/church-calendar/calendar.db`, owned by
`calendar`), and start it. Saved tokens are encrypted with `secrets.key`, kept
next to the database: copy it along too, or enter those tokens again after the
move. Droplet backups cover all the apps on a shared droplet.

## License

MIT. See `LICENSE`.
