# Church Calendar: sign-up links

The design is in `../.claude/plans/event-signups.md`. The event planner
owns sign-up forms and every answer (`../event_planning/SIGNUPS_PLAN.md`).
The calendar keeps only a link and a state on the booking, shows a
**Sign up** button, and passes the link on in its read API, so the church
website gets it from here.

Status: steps 1 and 2 built (2026-10-15), migration `004_signup_links.sql`.

## What arrives

The push body (`PUT /api/v1/bookings/{source}/{source_id}`) gains an
optional object. No `signup`, or `null`, clears it.

```json
"signup": {
  "label": "Sign up for the cook-off",
  "url": "https://events.example.org/signup/k3J9xQ",
  "state": "open",
  "closes_at": "2026-11-01T23:59"
}
```

`state` is `open`, `full` or `closed`. The calendar never fetches the form,
counts, names or answers.

## Data (migration `0NN_signup_links.sql`, the next free number)

```sql
ALTER TABLE bookings ADD COLUMN signup_label TEXT NOT NULL DEFAULT '';
ALTER TABLE bookings ADD COLUMN signup_url TEXT NOT NULL DEFAULT '';  -- '' = no sign-up
ALTER TABLE bookings ADD COLUMN signup_state TEXT NOT NULL DEFAULT ''
    CHECK (signup_state IN ('', 'open', 'full', 'closed'));
ALTER TABLE bookings ADD COLUMN signup_closes_at TEXT NOT NULL DEFAULT '';
```

## Rules

- Only app bookings (`events` for now) carry a sign-up. Hand-made and
  request bookings never do; **Make this an event** is the way to add one.
- **Visibility decides who sees the link**, like the description: public
  bookings show it to everyone; private ones only to members and tokens
  with private access; locked ones never (the event planner doesn't send
  it for them either, and the calendar drops it if it arrives).
- The URL must be `https://` (or `http://` in dev) on the event planner's
  address when the **Event planner** connection is set; otherwise it's
  dropped, not refused (pushes are never refused).
- A `closes_at` in the past shows as closed even if no push has come yet.

## Where it shows

| Place | What |
|-------|------|
| `/calendar/{id}` | A **Sign up** button (the label) when open; "Full" or "Sign-ups closed" otherwise |
| Month and agenda views | A small "Sign-ups open" mark on the event |
| `GET /api/v1/events`, `/api/v1/events/{id}` | `"signup": {"label", "url", "state", "closes_at"}` or no key, under the same visibility rules |
| `.ics` | The URL added to `DESCRIPTION` when open ("Sign up: …"). Not in busy blocks |

## Steps

### 1. Store and show the link
- [x] Migration `0NN_signup_links.sql`, the next free number; push decodes `signup`, checks the URL, drops it for locked and non-app bookings
- [x] Button on `/calendar/{id}`, mark in month/agenda views, `closes_at` in the past reads as closed
- [x] Read API and `.ics` field, with visibility
- [x] Tests: push with and without `signup` (cleared), locked drops it, private hidden from visitors and tokens without private access, wrong-host URL dropped, past `closes_at` shows closed

### 2. Website
- [x] Note in `README.md`'s read API section: `signup` is optional; show a button to `url` when `state` is `open`
