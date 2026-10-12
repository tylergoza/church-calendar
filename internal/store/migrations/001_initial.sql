-- People. With single sign-on this is a local cache of User Management's
-- users (keyed by sso_subject), so every users(id) reference keeps working.
--   member     sees private bookings in full (not locked ones), asks for rooms
--   scheduler  also makes and edits bookings, answers requests, manages
--              rooms, sees and resolves conflicts
--   admin      also users, API tokens, connected apps, settings
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT NOT NULL DEFAULT '',
    password_hash TEXT,                        -- NULL: can't sign in locally
    role          TEXT NOT NULL DEFAULT 'member'
                  CHECK (role IN ('member', 'scheduler', 'admin')),
    sso_subject   TEXT UNIQUE,                 -- User Management's user ID
    active        INTEGER NOT NULL DEFAULT 1,  -- 0: access removed, row kept for history
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    token            TEXT PRIMARY KEY,
    user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token       TEXT NOT NULL,
    expires_at       TEXT NOT NULL,
    sso_grant        TEXT,   -- the grant behind an SSO session; NULL for local login
    grant_checked_at TEXT,
    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX sessions_user ON sessions(user_id);

-- Site settings and connections to other apps. Secret values are sealed
-- ("enc:v1:...") with the key in secrets.key next to the database.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- A room or space that can be booked.
CREATE TABLE rooms (
    id               INTEGER PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    tracker_place_id INTEGER UNIQUE,              -- NULL when typed in by hand
    tracker_path     TEXT NOT NULL DEFAULT '',    -- cached "Main building › Lifecenter"
    capacity         INTEGER CHECK (capacity IS NULL OR capacity >= 0),
    bookable         INTEGER NOT NULL DEFAULT 1,  -- shown on the request form
    needs_approval   INTEGER NOT NULL DEFAULT 1,  -- requests wait for a scheduler
    notes            TEXT NOT NULL DEFAULT '',
    archived         INTEGER NOT NULL DEFAULT 0
);

-- Hand-made repeats. Each date is a real booking, made ahead of time by
-- copying the series' template booking (as the event planner does).
CREATE TABLE series (
    id                  INTEGER PRIMARY KEY,
    freq                TEXT    NOT NULL CHECK (freq IN ('daily', 'weekly', 'monthly')),
    every               INTEGER NOT NULL DEFAULT 1 CHECK (every > 0),  -- every N days/weeks/months
    weekdays            TEXT    NOT NULL DEFAULT '',  -- weekly: "0,3" (0 = Sunday); '' = starts_on's weekday
    month_day           INTEGER CHECK (month_day BETWEEN 1 AND 31),  -- monthly by date (short months use their last day)
    month_week          INTEGER CHECK (month_week IN (1, 2, 3, 4, -1)),  -- monthly by weekday: 1st..4th, -1 = last
    month_weekday       INTEGER CHECK (month_weekday BETWEEN 0 AND 6),
    starts_on           TEXT    NOT NULL,             -- YYYY-MM-DD, the rule's anchor
    until               TEXT    NOT NULL DEFAULT '',  -- last date it may fall on; '' = no end date
    max_count           INTEGER CHECK (max_count IS NULL OR max_count > 0),  -- or stop after this many
    -- The booking new dates are copied from. NULL (it was deleted): the
    -- latest remaining date is used instead.
    template_booking_id INTEGER REFERENCES bookings(id) ON DELETE SET NULL,
    -- Dates exist up to this one; topped up as time passes.
    generated_through   TEXT    NOT NULL DEFAULT '',
    created_at          TEXT    NOT NULL DEFAULT (datetime('now')),
    CHECK (freq <> 'monthly' OR (month_day IS NULL) <> (month_week IS NULL)),
    CHECK ((month_week IS NULL) = (month_weekday IS NULL))
);

-- Dates skipped (their booking deleted on its own), so topping up doesn't
-- bring them back.
CREATE TABLE series_skips (
    series_id INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    date      TEXT    NOT NULL,
    PRIMARY KEY (series_id, date)
);

-- One block of time on the calendar. Bookings from other apps are a thin
-- copy: times, rooms, title, public description, visibility and a link
-- back. Never their notes, tasks, helpers, cast or needs.
CREATE TABLE bookings (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL,   -- 'calendar', 'request', 'events', 'productions'
    source_id       TEXT,            -- the other app's ID, e.g. '12' or 'date-88'; NULL for calendar/request
    series_id       INTEGER REFERENCES series(id) ON DELETE SET NULL,  -- hand-made repeats only
    occurrence_date TEXT NOT NULL DEFAULT '',  -- the date the series rule gave it, even if moved
    title           TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT '',
    starts_on       TEXT NOT NULL,             -- YYYY-MM-DD
    start_time      TEXT NOT NULL DEFAULT '',  -- HH:MM, '' = all day
    ends_on         TEXT NOT NULL DEFAULT '',  -- '' = same day
    end_time        TEXT NOT NULL DEFAULT '',
    visibility      TEXT NOT NULL DEFAULT 'private'
                    CHECK (visibility IN ('public', 'private', 'locked')),
    description     TEXT NOT NULL DEFAULT '',  -- the public description only
    url             TEXT NOT NULL DEFAULT '',  -- back to the owning app's page
    status          TEXT NOT NULL DEFAULT 'confirmed'
                    CHECK (status IN ('confirmed', 'tentative', 'cancelled')),
    created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,  -- hand-made ones
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (source, source_id),
    CHECK ((source IN ('calendar', 'request')) = (source_id IS NULL))
);
CREATE INDEX bookings_dates ON bookings(starts_on, ends_on);
CREATE INDEX bookings_updated ON bookings(source, updated_at);
CREATE UNIQUE INDEX bookings_occurrence ON bookings(series_id, occurrence_date) WHERE series_id IS NOT NULL;

CREATE TABLE booking_rooms (
    booking_id INTEGER NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    room_id    INTEGER REFERENCES rooms(id) ON DELETE SET NULL,  -- NULL: a place we don't know
    room_name  TEXT NOT NULL,   -- as sent, so unknown places still show
    PRIMARY KEY (booking_id, room_name)
);
CREATE INDEX booking_rooms_room ON booking_rooms(room_id);

-- Pairs of bookings a scheduler marked "that's fine". Conflicts themselves
-- are worked out when read, never stored. low_id < high_id.
CREATE TABLE conflict_ok (
    low_id     INTEGER NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    high_id    INTEGER NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    marked_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (low_id, high_id),
    CHECK (low_id < high_id)
);

-- Someone asking for a room. Approving it makes a booking (source 'request').
-- code is the random part of the /request/{id}?code=... status link the
-- person is given.
CREATE TABLE requests (
    id             INTEGER PRIMARY KEY,
    code           TEXT NOT NULL,
    name           TEXT NOT NULL,
    contact        TEXT NOT NULL,             -- email or phone
    user_id        INTEGER REFERENCES users(id) ON DELETE SET NULL,
    title          TEXT NOT NULL,
    starts_on      TEXT NOT NULL,
    start_time     TEXT NOT NULL DEFAULT '',
    ends_on        TEXT NOT NULL DEFAULT '',
    end_time       TEXT NOT NULL DEFAULT '',
    expected_count INTEGER CHECK (expected_count IS NULL OR expected_count >= 0),
    details        TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'waiting'
                   CHECK (status IN ('waiting', 'approved', 'declined', 'withdrawn')),
    answer         TEXT NOT NULL DEFAULT '',  -- note back to the person
    booking_id     INTEGER REFERENCES bookings(id) ON DELETE SET NULL,
    decided_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX requests_status ON requests(status, starts_on);

CREATE TABLE request_rooms (
    request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
    room_id    INTEGER NOT NULL REFERENCES rooms(id),
    PRIMARY KEY (request_id, room_id)
);

-- API tokens. kind 'read' = read API; kind 'source' = an app that pushes
-- its own bookings. Only a hash is kept: the token is shown once, and its
-- first few characters (prefix) tell tokens apart afterwards.
CREATE TABLE tokens (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,             -- "Event planner", "Church website"
    kind        TEXT NOT NULL CHECK (kind IN ('read', 'source')),
    source      TEXT,                      -- 'events', 'productions' for kind 'source'
    see_private INTEGER NOT NULL DEFAULT 0,
    hash        TEXT NOT NULL UNIQUE,      -- sha256
    prefix      TEXT NOT NULL,
    created_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    last_used   TEXT,
    revoked_at  TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK ((kind = 'source') = (source IS NOT NULL))
);
