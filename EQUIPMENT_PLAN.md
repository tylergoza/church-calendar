# Church Calendar: equipment and services on bookings

The cross-app design is in `../.claude/plans/bookable-equipment.md`; this
is the calendar's part. When someone books a room they can also ask for
equipment (tables, chairs, the inflatables) and services (Sound,
Lighting). Everything still works with the calendar alone: without the
tracker, an admin makes the equipment list by hand and marks it **In use**
or **In storage**.

This replaces "Booking equipment" under *Things not to do (yet)* in
`PLAN.md`. The planners' needs still never reach the calendar.

Status: planned (2026-10-13).

## What changes, in short

| | Now | After |
|--|-----|-------|
| Asking for equipment | Typed into **Details** | Picked on `/request` with a number (or yes + a note for services) |
| Where the list comes from | n/a | Tracker products set **On the church calendar**, synced every 15 minutes; or made by hand when running alone |
| Where it's offered | n/a | Only with the rooms picked for it. A building picked means the building booked whole, not the rooms inside. None picked = not offered |
| Not enough to go round | n/a | Never refused. Overlapping asks over the total are flagged **short by N** for schedulers |
| Members | Free rooms are booked | Unchanged: free rooms are booked, even when equipment is short (flagged) |
| Where it is now | n/a | From the tracker's item places; alone, an **In use / In storage** toggle on each booking's line |
| Who sees it | n/a | Schedulers, admins, the owner; the read API with a token. Never busy blocks or `.ics` |

## Steps

### 1. Migration `003_equipment.sql`
- [ ] `equipment`: `id`, `name` (unique, nocase), `tracker_product_id` (unique, NULL if hand-made), `mode TEXT NOT NULL CHECK (mode IN ('count','service'))`, `total INTEGER NOT NULL DEFAULT 0` (unused for services), `notes` (shown on the form), `archived`
- [ ] `equipment_rooms(equipment_id, room_id)`: rooms it's offered with. None = not offered
- [ ] `request_equipment(request_id, equipment_id, quantity, note)` and `booking_equipment(booking_id, equipment_id, name, quantity, note, in_use INTEGER NOT NULL DEFAULT 0, in_use_changed_at, in_use_changed_by)`; `name` kept like `booking_rooms.room_name`. `quantity >= 1`; 1 for services. `in_use` is only used when the tracker connection is off
- [ ] `store.Equipment`, loads and saves; `go test ./...` with a test that runs 001 → 003 on a copy holding sample rows

### 2. Equipment by hand (admins)
- [ ] `/equipment` (`requireAdmin`, under rooms in the admin menu): list, new, edit, archive. Fields: name, **Ask how many** (with total) or **Ask yes or no** (a service), **Offered with** (bookable room checkboxes grouped by building, a bookable building pickable on its own for whole-building bookings, using the `room-pick` partial), notes. Nothing ticked shows "Not offered anywhere yet"
- [ ] Imported equipment is read-only here, with "Set in the tracker" and a link to the product
- [ ] Tests: admin only, name unique, service has no total, no rooms = not on the form

### 3. Asking for it
- [ ] Offered with a booking: equipment whose offered rooms include one of the picked rooms. Exact match only, no building tree: a building offer applies when the building is picked whole, and a room inside it only gets what is offered with that room
- [ ] `/request` (`pages/requests/new.html`): an **Equipment and help** section under the rooms. Counted: a number box (`min=0`, `max=total`) and "28 of 40 not asked for then". Service: a checkbox and a note box. The list follows the picked rooms (the `availability` Stimulus controller already reacts to changes; extend it or add an `equipment` controller, no build step). Without JavaScript every equipment offered with any bookable room shows and the server checks it against the rooms
- [ ] `/request/availability` also answers how much of each counted item is not yet asked for at those times
- [ ] Shortage sum: for counted equipment, the sum of `booking_equipment.quantity` on overlapping bookings that aren't `cancelled` (ignoring the booking itself). Over `total` = **short by N**. Worked out when read, not stored, like conflicts
- [ ] `readRequest`: only accepts equipment offered with the picked rooms; quantity 1…total; note up to 200 characters; nothing picked is fine
- [ ] `insertRequest` writes `request_equipment`; `insertRequestBooking` copies it to `booking_equipment`
- [ ] **Members**: booked at once when the rooms are free, as now, whatever the equipment. A short line saves and the confirmation says "We may be short of chairs; a scheduler will be in touch". `ErrRoomsBusy` stays about rooms only
- [ ] **Visitors**: saved as asked, pending as now, with the same note
- [ ] Owner edit (`/request/{id}/edit`) and scheduler edit (`/bookings/{id}/edit` for request and hand-made bookings) can change equipment the same way
- [ ] Hand-made bookings (`/bookings/new`) get the same section
- [ ] Tests: offers match picked rooms exactly (a building offer shows for a whole-building booking, not for a room inside it, and the other way round); a member over the total is booked and flagged; pending and tentative count toward the sum; cancelled frees it; editing a booking doesn't count itself; services are never short; a busy room is still refused

### 4. Equipment from the tracker
Needs `../maintenance_tracking/EQUIPMENT_PLAN.md` step 1 (`booking`, `offered_in` in `GET /api/v1/products?booking=1`).
- [ ] `internal/tracker`: `Products(booking bool)` → `GET /products?booking=1`, decoding `id`, `name`, `booking`, `total`, `notes`, `offered_in`; `Items(product)` → `GET /items?product=`, decoding `quantity` and `place{id,path}`
- [ ] `SyncTracker` also syncs equipment: adds and updates offered products (name, mode, total, offered rooms via `rooms.tracker_place_id`; an offered place that isn't a calendar room yet is skipped), archives equipment whose product is no longer offered or gone. A failed call logs and tries again, never archives
- [ ] **Where it is now**: the sync also keeps each counted equipment's places from its items (`equipment_places(equipment_id, place_path, room_id NULL, quantity)`, replaced on every sync). A place that's a calendar room links to it
- [ ] Rooms → **Import from tracker** page also shows "N pieces of equipment come from the tracker" with a sync button
- [ ] Tests: sync adds/updates/archives, offered places map to rooms, unknown places skipped, places replaced each sync, failed call archives nothing and keeps the last places, a booking keeps its equipment name after archiving

### 5. Schedulers and owners see it
- [ ] `/requests/{id}` and `/calendar/{id}`: the equipment asked for, with **short by N** in red where it's over (CSS class, no inline styles), and which other bookings asked for it at the same time
- [ ] `/calendar/{id}` and `/my/bookings`: equipment for schedulers, admins and the owner only
- [ ] **Where it is now** on `/equipment` and on each counted line: with the tracker, its places from step 4 ("30 in Life Center, 20 in Storage B", "Set in the tracker"). Alone, "In use in Life Center (since Sat 18 Oct, booking …)" from the lines whose toggle is on, the rest "In storage"
- [ ] **In use / In storage** toggle (schedulers, tracker connection off only) on each counted line of a booking on `/calendar/{id}` and on `/setups`. It stays in use until someone turns it off, whatever the booking's times. A small form POST with CSRF; no JavaScript needed
- [ ] `/setups` for schedulers: the next two weeks of bookings with equipment, by day, with **short by N**; then **Still set up**: lines in use (alone) or equipment sitting in a booked room (tracker), so it's clear what needs taking down. The tracker has its own version; this one is for schedulers and for when the calendar runs alone
- [ ] A **Short** filter on `/requests` lists upcoming bookings with a short line
- [ ] Tests: visitors and other members never see equipment; busy blocks and `.ics` never include it; the toggle is scheduler-only and hidden with the tracker on; in use stays on after the booking ends

### 6. In the read API (for the tracker)
- [ ] `GET /api/v1/events` adds `equipment: [{name, tracker_product_id, quantity, note, short}]` on bookings, only for token callers and only on bookings the token can see in full (not busy blocks)
- [ ] Tests: no equipment without a token or on busy blocks; phone numbers still never in the API

### 7. Docs
- [ ] `PLAN.md`: data model, pages table
- [ ] README: running-alone table ("Products set **On the church calendar** in the tracker can be asked for with a booking in the rooms they're offered with; where they are comes from the tracker"), the `/equipment` and `/setups` pages, the toggle when alone, the read API field
- [ ] `.claude/CLAUDE.md`: the rules: equipment shortages are flagged, never refused (pending and tentative count toward them); no set-up/tear-down windows, where equipment is comes from the tracker or the toggle; equipment is never public
