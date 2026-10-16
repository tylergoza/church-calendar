-- Sign-up links pushed by the event planner (SIGNUPS_PLAN.md step 1). The
-- calendar keeps only the label, address, state and close time; the form,
-- counts and answers stay in the event planner.
ALTER TABLE bookings ADD COLUMN signup_label TEXT NOT NULL DEFAULT '';
ALTER TABLE bookings ADD COLUMN signup_url TEXT NOT NULL DEFAULT '';  -- '' = no sign-up
ALTER TABLE bookings ADD COLUMN signup_state TEXT NOT NULL DEFAULT ''
    CHECK (signup_state IN ('', 'open', 'full', 'closed'));
ALTER TABLE bookings ADD COLUMN signup_closes_at TEXT NOT NULL DEFAULT '';  -- local "2026-11-01T23:59", '' = no close time
