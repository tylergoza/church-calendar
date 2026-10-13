# Church Calendar: project notes

The one calendar and room-booking app. Sibling of `../event_planning`,
`../production_planning`, `../maintenance_tracking` and `../user_management`.
Same stack and conventions: Go + SQLite (modernc, no CGO) + server-rendered
templates + vendored Stimulus with the no-build autoloader. No Node, no
bundler, no new dependencies. Copy shared pieces (auth, CSRF, layout, SSO,
Ansible, calendar views) from `../event_planning`, the newest sibling.

- `PLAN.md` is the design and the TODO list. Tick its checkboxes as steps
  are done.
- Every app must run alone. Links to other apps are optional and off
  unless their URL and token are set (the `Configured()` pattern).
- Shared kinds of data (places/rooms, items, events, users) keep the same
  shape in every app, matching the owning app's API. A thin local version
  is a subset of the full one, so when the owning app is connected the
  local rows start referencing it (its ID plus a cached name) instead of
  being rebuilt.
- The calendar keeps a thin copy of other apps' events (times, rooms,
  title, public description, visibility, link). It never edits them, and
  never holds notes, tasks, helpers, cast or needs.
- Pushes from apps are never refused for conflicts: always 200 with a
  `conflicts` list. Conflicts are computed when read, not stored.
- Booking status `pending` (a visitor's request waiting for a scheduler)
  and `tentative` (pencilled in) block rooms exactly like `confirmed`;
  only `cancelled` frees them. Members' requests are never pending: free
  is booked, busy is refused. Busy checks and conflicts match a room's
  family (the room, the buildings it's in, the rooms inside it), where a
  building and a room inside only count together if the inner room is
  bookable. A bookable building is booked whole; it doesn't list its rooms.
- Visibility rules must match the event planner's (public / private /
  locked = busy). Never show more than rooms and times for busy blocks.
- Cookies `cal_*`, tokens `cal_…`; local port 8120 (tracker 8080, planner
  8090, User Management 8100, event planner 8110).
- SSO client ID `calendar`; secret `UM_CALENDAR_SECRET` (Keychain
  `um-calendar`). Local login when the SSO connection is off.
- Secrets never go in files or `export` lines in docs; the user keeps them
  in the macOS Keychain and runs make through `~/.zshrc` helpers.
- CSP is `style-src 'self'`: no inline `style=` attributes in templates.
- Deploys onto the maintenance tracker's droplet as `calendar.<dns_zone>`;
  the main Caddyfile text must stay identical in every repo's deploy.yml.
- The user runs all `git commit`s themselves (YubiKey signing). Don't commit.
- Connections (design in `../event_planning/CONNECTIONS_PLAN.md`): links to
  other apps are admin settings on `/admin/connections`; env vars override
  them per whole connection. Secrets encrypted with `secrets.key`.
- `BOOKING_PLAN.md`: this app's part of bookable rooms / pending bookings / unscheduled events (design in `../.claude/plans/bookable-rooms-and-pending-bookings.md`). Tick its checkboxes as steps are done.
- `EQUIPMENT_PLAN.md`: this app's part of equipment and services (Sound, Lighting) asked for with room bookings (design in `../.claude/plans/bookable-equipment.md`). Tick its checkboxes as steps are done.
