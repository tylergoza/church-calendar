# Church Calendar: pending bookings, bookable places, owners and cancelling

The cross-app design is in
`../.claude/plans/bookable-rooms-and-pending-bookings.md`; this is the
calendar's part. Everything still works with the calendar alone: without
the tracker, rooms (and their building) are set by hand.

Status: built 2026-10-13. Imported rooms follow the tracker's `bookable` once `../maintenance_tracking/BOOKING_PLAN.md` step 1 is deployed; until then the calendar keeps its own flags.

From the first deploy on, `001_initial.sql` is frozen, so all of this goes
in new migrations (`002_…`). Changing a CHECK needs a table rebuild with
foreign keys off (the runner's `noForeignKeys` flag, `internal/store/store.go`).

## What changes, in short

| | Now | After |
|--|-----|-------|
| A visitor's request | A `requests` row; no booking until approved; doesn't block the room | A `requests` row **and** a `pending` booking at once; blocks the room |
| A signed-in member's request | Booked at once only if every room has `needs_approval` off and is free; otherwise waits | **Booked at once** (`confirmed`) whenever every room is free. Refused, showing what's in the way, if any room is busy. Never pending. `needs_approval` is retired |
| Visitors | Name + "email or phone", only if public requests is on (off by default) | Name + **phone** required, email optional; public requests on by default for new installs |
| Signed-in member | Can withdraw while waiting | Can **change** or **cancel** their bookings |
| Schedulers/admins | Edit or hard-delete hand-made bookings | Also **Cancel** (kept, with a note to the person) any hand-made or request booking |
| Bookable | Checkbox in the calendar | From the tracker for imported rooms, sync adds bookable places by itself |
| Buildings | Just another room | A building is busy when a room in it is, and the other way round |

## Steps

### 1. Migration `002_pending_bookings.sql`
- [x] Rebuild `bookings` with `status IN ('confirmed','tentative','pending','cancelled')`; add `cancel_note TEXT NOT NULL DEFAULT ''`, `cancelled_by INTEGER REFERENCES users(id) ON DELETE SET NULL`
- [x] Rebuild `requests` with `status IN ('waiting','approved','declined','withdrawn','cancelled')`; add `phone TEXT NOT NULL DEFAULT ''`; `contact` stays (now "email, optional")
- [x] `rooms`: add `kind TEXT NOT NULL DEFAULT ''` (`building`, `room`, `area`, or '' for hand-made) and `parent_id INTEGER REFERENCES rooms(id) ON DELETE SET NULL`
- [x] Backfill: every `waiting` request gets a `pending` booking (source `request`, private, its rooms), and `requests.booking_id` points to it. Waiting requests from members stay waiting for a scheduler (they were sent under the old rule)
- [x] `rooms.needs_approval` is no longer used. Leave the column (001 is frozen), drop it from the room form, `store.Room` and the README
- [x] `store.BookingStatuses`, `store.RequestStatuses` updated; `go test ./...` with a test that runs 001 → 002 on a copy holding sample rows

### 2. Pending bookings on the calendar
- [x] `handleRequestCreate` for **visitors**: one transaction makes the request and its `pending` booking (title from the request, private, `created_by` NULL)
- [x] `handleRequestCreate` for **signed-in members**: `bookNow` becomes "every room is free" (no `needs_approval` check). Free: one transaction makes the request (status `approved`, `decided_by` NULL) and a `confirmed` booking (private, `created_by` the member). Busy: nothing is saved and the form comes back with the clashing times (busy blocks only, never titles), as the live availability already shows. The busy check runs inside the transaction so two members can't take the same slot at once. "Busy" includes `pending` and `tentative` bookings and, after step 3, the room's building and the rooms inside it
- [x] `ApproveRequest` changes the existing booking to `confirmed` (and visibility as chosen) instead of inserting one; double-approve still 409. `DeclineRequest` / `WithdrawRequest` change the booking to `cancelled`
- [x] Busy checks: `spansSQL`, `BusyInRooms`, `BusyInRoomsNamed` already skip only `cancelled`, so pending counts with no change. Add tests that prove a waiting request blocks `/request/availability`, `GET /api/v1/availability`, `bookNow` and shows in `/conflicts`
- [x] Views: `pending` and `tentative` get a hatched style and a **Pending** / **Pencilled in** label in the month grid, agenda, room week view, `/calendar/{id}` and `partials/bookings.html` (CSS classes only, CSP has no inline styles). Busy blocks keep the mark ("Lifecenter · busy, pending")
- [x] Read API: `status` already in the JSON; `.ics` sends `STATUS:TENTATIVE` for `pending` too, including busy entries
- [x] Calendar filter: **Show pending** on by default; off hides `pending` and `tentative`
- [x] `/requests` (schedulers): the Waiting tab is the approval queue; the request page shows the phone number as a `tel:` link and the email as `mailto:`
- [x] Tests: the visibility table for pending (visitors see busy + mark only), approve flips the same booking, decline/withdraw free the room; a member's free booking is `confirmed` with no request in the Waiting tab; a member's clashing booking is refused and saves nothing; a member can't book over a visitor's `pending` one

### 3. Bookable from the tracker, and buildings
Needs `../maintenance_tracking/BOOKING_PLAN.md` step 1 (`bookable` in `GET /api/v1/places`).
- [x] `internal/tracker.Place` gains `Bookable`; `store.TrackerPlace` gains `Kind`, `ParentID`, `Bookable` (no longer dropped in `handlers_import.go`)
- [x] Import writes `rooms.kind`, `rooms.parent_id` (the calendar room for the nearest imported ancestor) and, for imported rooms, `rooms.bookable` from the tracker. Re-import keeps them in step
- [x] **Tracker sync**: a background job every 15 minutes (and on the Connections **Test**) when the tracker connection is on. It runs the same update as a re-import, and **adds** places marked bookable that aren't rooms yet. It doesn't add places that aren't bookable; a person still adds those with **Import from tracker**. A failed sync logs and tries again next time, never archives
- [x] Room form: for an imported room the **Bookable** checkbox is read-only with "Set in the tracker" and a link to the place there; hand-made rooms keep the checkbox and get a **Inside** (parent room) select
- [x] Building tree in busy checks: a recursive CTE `room_family(room_id, related_id)` (the room, its ancestors and its descendants). `spansSQL` users and `Store.conflicts` match on the family instead of the same `room_id`. Booking "Main building" makes "Nursery" busy and the other way round; two rooms in one building do not clash with each other
- [x] `/request` lists bookable rooms grouped under their building; a building that is bookable can be picked whole
- [x] Tests: sync adds bookable, updates flags/names/parents, never adds unbookable, doesn't archive on a failed call; family conflicts both ways; siblings don't clash; hand-made parent

### 4. People change or cancel their own bookings
- [x] `/my/bookings` (signed-in): their requests and bookings, upcoming first, with status. Linked from the menu and from `/request`
- [x] `/request/{id}/edit` for the owner (`ownRequest` with a user, not the code link): title, rooms, times, count, details. The booking stays `confirmed`. Changed rooms or times are checked like a new booking (ignoring the booking itself): free saves, busy is refused with what's in the way and nothing changes. A member's old request still waiting from before this change can be edited the same way, and is booked at once when free
- [x] **Cancel** for the owner: request `withdrawn`, booking `cancelled`. Visitors keep **Withdraw** on the code link while it's pending; they can't edit
- [x] Owner edits never touch a booking that a planner took over (`replaces` moved the request to an app booking): the page says it's now part of an event and links there
- [x] Tests: owner edit/cancel, other members refused (403), edit into a busy slot refused and unchanged, edit that only overlaps itself saves, visitor code link can withdraw but not edit

### 5. Schedulers and admins cancel any booking
- [x] **Cancel booking** on `/calendar/{id}` and `/bookings/{id}/edit` for hand-made and request bookings (`requireScheduler`; `admin` includes it): status `cancelled`, `cancel_note`, `cancelled_by`; a request behind it becomes `cancelled` with the note as its `answer`, shown on the person's status page. A repeating booking offers this date / this and later, as delete does
- [x] Cancelled bookings leave the calendar, availability and conflicts, and can be found under `/requests` (Cancelled tab) and on the booking page. Hard delete stays for mistakes
- [x] Pushed bookings (`events`, `productions`) show "Cancel it in the event planner / production planner" with the link instead of the button. The calendar never edits another app's bookings
- [x] Tests: cancel by scheduler and admin, member refused, pushed booking refused, request status page shows the note

### 6. Visitors: name and phone
- [x] `readRequest`: visitors must give a name and a phone (7–15 digits once spaces, dashes, dots, brackets and a leading `+` are removed); email optional and checked loosely when given. Signed-in people: name prefilled, phone and email optional
- [x] Form: separate **Phone** (`type="tel"`, `autocomplete="tel"`) and **Email (optional)** fields; the text says a scheduler will call or text to confirm
- [x] Public requests on by default: `PublicRequests()` treats a never-saved setting as on. An admin who saved "off" keeps it off
- [x] Phone numbers never leave the calendar: not in the read API, `.ics`, conflicts or busy blocks; only on `/requests/{id}` for schedulers and the person's own pages
- [x] Tests: phone required for visitors only, phone format, honeypot and rate limit unchanged, phone not in API output

### 7. Docs
- [x] `PLAN.md`: data model, roles table ("member: change or cancel their own bookings"), pages table, a pointer to this file
- [x] README: running-alone table ("Rooms marked bookable in the tracker appear on the request form"), visitor booking
- [x] `.claude/CLAUDE.md`: the `pending` status rule (pending and tentative block rooms like confirmed ones)
