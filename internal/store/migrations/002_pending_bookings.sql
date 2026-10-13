-- foreign_keys: off
-- Pending bookings, cancelling with a note, visitors' phone numbers and
-- rooms inside buildings (BOOKING_PLAN.md step 1). Changing a CHECK needs
-- the table rebuilt, so bookings and requests are copied across.
--
-- rooms.needs_approval is no longer used: members book straight away when
-- the rooms are free, and visitors always wait. The column stays because
-- 001 is frozen.

-- bookings: new statuses 'pending' (a visitor's request waiting for a
-- scheduler), and a note and who cancelled it.
CREATE TABLE bookings_new (
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
    -- pending: a visitor's request waiting for a scheduler. tentative: a
    -- date pencilled in. Both block their rooms like confirmed ones.
    status          TEXT NOT NULL DEFAULT 'confirmed'
                    CHECK (status IN ('confirmed', 'tentative', 'pending', 'cancelled')),
    created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,  -- hand-made ones
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    cancel_note     TEXT NOT NULL DEFAULT '',  -- to the person, when a scheduler cancels it
    cancelled_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    UNIQUE (source, source_id),
    CHECK ((source IN ('calendar', 'request')) = (source_id IS NULL))
);
INSERT INTO bookings_new (id, source, source_id, series_id, occurrence_date, title, kind, starts_on, start_time,
    ends_on, end_time, visibility, description, url, status, created_by, created_at, updated_at)
SELECT id, source, source_id, series_id, occurrence_date, title, kind, starts_on, start_time,
    ends_on, end_time, visibility, description, url, status, created_by, created_at, updated_at FROM bookings;
DROP TABLE bookings;
ALTER TABLE bookings_new RENAME TO bookings;
CREATE INDEX bookings_dates ON bookings(starts_on, ends_on);
CREATE INDEX bookings_updated ON bookings(source, updated_at);
CREATE UNIQUE INDEX bookings_occurrence ON bookings(series_id, occurrence_date) WHERE series_id IS NOT NULL;

-- requests: 'cancelled' (a scheduler cancelled the booking) and a phone
-- number. contact is now the email, optional.
CREATE TABLE requests_new (
    id             INTEGER PRIMARY KEY,
    code           TEXT NOT NULL,
    name           TEXT NOT NULL,
    contact        TEXT NOT NULL,             -- email, optional ('' when not given)
    phone          TEXT NOT NULL DEFAULT '',  -- required from visitors; never leaves the calendar
    user_id        INTEGER REFERENCES users(id) ON DELETE SET NULL,
    title          TEXT NOT NULL,
    starts_on      TEXT NOT NULL,
    start_time     TEXT NOT NULL DEFAULT '',
    ends_on        TEXT NOT NULL DEFAULT '',
    end_time       TEXT NOT NULL DEFAULT '',
    expected_count INTEGER CHECK (expected_count IS NULL OR expected_count >= 0),
    details        TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'waiting'
                   CHECK (status IN ('waiting', 'approved', 'declined', 'withdrawn', 'cancelled')),
    answer         TEXT NOT NULL DEFAULT '',  -- note back to the person
    booking_id     INTEGER REFERENCES bookings(id) ON DELETE SET NULL,
    decided_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO requests_new (id, code, name, contact, user_id, title, starts_on, start_time, ends_on, end_time,
    expected_count, details, status, answer, booking_id, decided_by, created_at)
SELECT id, code, name, contact, user_id, title, starts_on, start_time, ends_on, end_time,
    expected_count, details, status, answer, booking_id, decided_by, created_at FROM requests;
DROP TABLE requests;
ALTER TABLE requests_new RENAME TO requests;
CREATE INDEX requests_status ON requests(status, starts_on);
CREATE INDEX requests_user ON requests(user_id);
CREATE INDEX requests_booking ON requests(booking_id);

-- rooms: the tracker's kind and the room it's inside (a building).
ALTER TABLE rooms ADD COLUMN kind TEXT NOT NULL DEFAULT '';  -- building, room, area; '' for hand-made
ALTER TABLE rooms ADD COLUMN parent_id INTEGER REFERENCES rooms(id) ON DELETE SET NULL;

-- Every waiting request gets its pending booking, so it blocks its rooms.
-- Waiting requests from members stay waiting for a scheduler: they were
-- sent under the old rule. created_by is the member, NULL for a visitor.
INSERT INTO bookings (source, title, starts_on, start_time, ends_on, end_time, visibility, status, created_by, created_at, updated_at)
SELECT 'request', 'request:' || id, starts_on, start_time, ends_on, end_time, 'private', 'pending', user_id, created_at, created_at
FROM requests WHERE status = 'waiting' AND booking_id IS NULL;
UPDATE requests SET booking_id = (SELECT b.id FROM bookings b WHERE b.source = 'request' AND b.title = 'request:' || requests.id)
WHERE status = 'waiting' AND booking_id IS NULL;
INSERT INTO booking_rooms (booking_id, room_id, room_name)
SELECT q.booking_id, rm.id, rm.name FROM requests q
JOIN request_rooms rr ON rr.request_id = q.id JOIN rooms rm ON rm.id = rr.room_id
JOIN bookings b ON b.id = q.booking_id AND b.title = 'request:' || q.id;
UPDATE bookings SET title = (SELECT q.title FROM requests q WHERE q.booking_id = bookings.id)
WHERE source = 'request' AND title LIKE 'request:%' AND status = 'pending';
