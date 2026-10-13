-- Equipment and services asked for with room bookings (EQUIPMENT_PLAN.md
-- step 1). A thin copy of the tracker's products set "On the church
-- calendar", or made by hand when the calendar runs alone.

-- equipment: count is asked how many (tables, chairs), up to total;
-- service is asked yes or no with a note (Sound, Lighting) and never runs
-- out, so total isn't used.
CREATE TABLE equipment (
    id                 INTEGER PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE COLLATE NOCASE,
    tracker_product_id INTEGER UNIQUE,              -- NULL when made by hand
    mode               TEXT NOT NULL CHECK (mode IN ('count', 'service')),
    total              INTEGER NOT NULL DEFAULT 0 CHECK (total >= 0),
    notes              TEXT NOT NULL DEFAULT '',    -- shown on the form
    archived           INTEGER NOT NULL DEFAULT 0
);

-- The rooms it's offered with. None: not offered anywhere yet. A building
-- here is offered only when the building is booked whole.
CREATE TABLE equipment_rooms (
    equipment_id INTEGER NOT NULL REFERENCES equipment(id) ON DELETE CASCADE,
    room_id      INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    PRIMARY KEY (equipment_id, room_id)
);
CREATE INDEX equipment_rooms_room ON equipment_rooms(room_id);

-- What a request asked for. quantity is 1 for a service.
CREATE TABLE request_equipment (
    request_id   INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
    equipment_id INTEGER NOT NULL REFERENCES equipment(id),
    quantity     INTEGER NOT NULL DEFAULT 1 CHECK (quantity >= 1),
    note         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (request_id, equipment_id)
);

-- What a booking asked for. name is kept like booking_rooms.room_name, so
-- it still shows after the equipment is archived. in_use (with when and
-- by whom) is the In use / In storage toggle, only used while the tracker
-- connection is off.
CREATE TABLE booking_equipment (
    booking_id        INTEGER NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    equipment_id      INTEGER NOT NULL REFERENCES equipment(id),
    name              TEXT NOT NULL,
    quantity          INTEGER NOT NULL DEFAULT 1 CHECK (quantity >= 1),
    note              TEXT NOT NULL DEFAULT '',
    in_use            INTEGER NOT NULL DEFAULT 0,
    in_use_changed_at TEXT NOT NULL DEFAULT '',
    in_use_changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    PRIMARY KEY (booking_id, equipment_id)
);
CREATE INDEX booking_equipment_equipment ON booking_equipment(equipment_id);

-- Where each counted piece of tracker equipment is now, from its items.
-- Replaced on every sync. room_id is set when the place is a calendar room.
CREATE TABLE equipment_places (
    equipment_id INTEGER NOT NULL REFERENCES equipment(id) ON DELETE CASCADE,
    place_path   TEXT NOT NULL,
    room_id      INTEGER REFERENCES rooms(id) ON DELETE SET NULL,
    quantity     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX equipment_places_equipment ON equipment_places(equipment_id);
