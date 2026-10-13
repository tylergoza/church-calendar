package server

import (
	"context"
	"errors"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
	"github.com/tylergoza/church-calendar/internal/tracker"
)

// SyncTracker brings the rooms in step with the maintenance tracker: what a
// re-import does (names, paths, kinds, buildings, bookable flags, archiving
// places that are gone) plus adding places the tracker marks bookable that
// aren't rooms yet. Places that aren't bookable are never added; a person
// does that with Import from tracker. Does nothing when the tracker isn't
// connected. A failed call is logged and left for next time: nothing is
// archived on a guess. Equipment follows the rooms (SyncTrackerEquipment);
// its trouble is logged and never fails the rooms' sync.
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
	s.SyncTrackerEquipment(ctx)
	return res, nil
}

// SyncTrackerEquipment brings equipment in step with the tracker's products
// that are set "On the church calendar": adds, updates and archives them
// (store.SyncEquipment), then keeps where each counted one is now from its
// items. Rooms come first, as offers and places are matched to them. Does
// nothing when the tracker isn't connected or is too old to offer
// equipment. A failed call is logged and returned, and archives nothing; a
// failed items call keeps that equipment's last places.
func (s *Server) SyncTrackerEquipment(ctx context.Context) (store.EquipmentSyncResult, error) {
	var res store.EquipmentSyncResult
	if !s.tracker().Configured() {
		return res, nil
	}
	fail := func(err error) (store.EquipmentSyncResult, error) {
		s.log.Error("tracker equipment sync", "err", err)
		return res, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	products, err := s.tracker().Products(ctx, true)
	if errors.Is(err, tracker.ErrTooOld) {
		return res, nil
	} else if err != nil {
		return fail(err)
	}
	list := make([]store.TrackerProduct, len(products))
	for i, p := range products {
		list[i] = store.TrackerProduct{ID: p.ID, Name: p.Name, Booking: p.Booking, Total: p.Total, Notes: p.Notes, OfferedIn: p.OfferedIn}
	}
	if res, err = s.store.SyncEquipment(list); err != nil {
		return fail(err)
	}
	if res.Changed() {
		s.log.Info("tracker equipment sync", "added", len(res.Added), "linked", len(res.Linked),
			"updated", len(res.Updated), "archived", len(res.Archived))
	}

	// Where each counted one is now.
	rooms, err := s.store.ListRooms(true)
	if err != nil {
		return fail(err)
	}
	roomOf := map[int64]int64{}
	for _, rm := range rooms {
		if rm.PlaceID != 0 {
			roomOf[rm.PlaceID] = rm.ID
		}
	}
	equipment, err := s.store.ListEquipment(false)
	if err != nil {
		return fail(err)
	}
	var failed error
	for _, e := range equipment {
		if !e.FromTracker() || e.Service() {
			continue
		}
		items, err := s.tracker().Items(ctx, e.ProductID)
		if err != nil {
			s.log.Error("tracker equipment places", "equipment", e.Name, "err", err)
			failed = err
			continue
		}
		var places []store.EquipmentPlace
		at := map[int64]int{} // place ID -> index in places
		for _, it := range items {
			if it.Quantity < 1 {
				continue
			}
			i, ok := at[it.Place.ID]
			if !ok {
				i = len(places)
				at[it.Place.ID] = i
				places = append(places, store.EquipmentPlace{Path: it.Place.Path, RoomID: roomOf[it.Place.ID]})
			}
			places[i].Quantity += it.Quantity
		}
		if err := s.store.SetEquipmentPlaces(e.ID, places); err != nil {
			return fail(err)
		}
	}
	return res, failed
}
