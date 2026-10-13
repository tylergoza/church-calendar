package server

import (
	"context"

	"github.com/tylergoza/church-calendar/internal/store"
)

// SyncTracker brings the rooms in step with the maintenance tracker: what a
// re-import does (names, paths, kinds, buildings, bookable flags, archiving
// places that are gone) plus adding places the tracker marks bookable that
// aren't rooms yet. Places that aren't bookable are never added; a person
// does that with Import from tracker. Does nothing when the tracker isn't
// connected. A failed call is logged and left for next time: nothing is
// archived on a guess.
func (s *Server) SyncTracker(ctx context.Context) (store.ImportResult, error) {
	var res store.ImportResult
	if !s.tracker().Configured() {
		return res, nil
	}
	places, err := s.trackerPlaces(ctx)
	if err != nil {
		s.log.Error("tracker sync", "err", err)
		return res, err
	}
	all := trackerPlaceList(places)
	have, err := s.store.ListRooms(true)
	if err != nil {
		s.log.Error("tracker sync", "err", err)
		return res, err
	}
	imported := map[int64]bool{}
	for _, rm := range have {
		if rm.PlaceID != 0 {
			imported[rm.PlaceID] = true
		}
	}
	var pick []int64
	for _, p := range all {
		if p.Bookable && bookableKind(p.Kind) && !imported[p.ID] {
			pick = append(pick, p.ID)
		}
	}
	res, err = s.store.ImportRooms(all, pick)
	if err != nil {
		s.log.Error("tracker sync", "err", err)
		return res, err
	}
	if n := len(res.Added) + len(res.Linked) + len(res.Renamed) + len(res.Moved) + len(res.Flagged) + len(res.Archived); n > 0 {
		s.log.Info("tracker sync", "added", len(res.Added), "linked", len(res.Linked), "renamed", len(res.Renamed),
			"moved", len(res.Moved), "flagged", len(res.Flagged), "archived", len(res.Archived))
	}
	return res, nil
}
