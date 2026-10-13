package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/tylergoza/church-calendar/internal/store"
)

// Room requests: anyone signed in (and visitors, when an admin turns
// public requests on) can ask for rooms at /request. Schedulers answer at
// /requests; approving makes a booking with source "request". People
// check back at /request/{id}?code=… (no email yet, see PLAN.md).

const publicRequestsSetting = "public_requests"

func requestURL(id int64, code string) string {
	return "/request/" + strconv.FormatInt(id, 10) + "?code=" + urlEscape(code)
}

// PublicRequests reports whether visitors may ask for rooms.
func (s *Server) PublicRequests() bool { return s.publicRequests.Load() }

// canRequest reports whether this person may ask for a room.
func (s *Server) canRequest(r *http.Request) bool {
	return currentUser(r) != nil || s.PublicRequests()
}

// bookableRooms is what the request form offers: rooms not archived and
// marked bookable.
func (s *Server) bookableRooms() ([]store.Room, error) {
	all, err := s.store.ListRooms(false)
	if err != nil {
		return nil, err
	}
	var out []store.Room
	for _, rm := range all {
		if rm.Bookable {
			out = append(out, rm)
		}
	}
	return out, nil
}

func (s *Server) requestForm(w http.ResponseWriter, r *http.Request, status int, req store.Request, picked []int64, errs []string) {
	rooms, err := s.bookableRooms()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	isPicked := map[int64]bool{}
	for _, id := range picked {
		isPicked[id] = true
	}
	u := currentUser(r)
	data := map[string]any{
		"Title": "Ask for a room", "Form": req, "Rooms": rooms, "Picked": isPicked, "Errors": errs,
		"PublicHeader": u == nil, "Today": s.today(),
	}
	if u != nil {
		mine, err := s.store.RequestsBy(u.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Mine"] = mine
	}
	w.Header().Set("Cache-Control", "private, no-store")
	s.render(w, r, status, "requests/new", data)
}

// sendToLogin is the answer to a visitor on a request page while public
// requests are off.
func (s *Server) sendToLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?next="+urlEscape("/request"), http.StatusSeeOther)
}

func (s *Server) handleRequestNew(w http.ResponseWriter, r *http.Request) {
	if !s.canRequest(r) {
		s.sendToLogin(w, r)
		return
	}
	req := store.Request{StartsOn: s.today()}
	if d := r.URL.Query().Get("date"); validDate(d) && d >= s.today() {
		req.StartsOn = d
	}
	if u := currentUser(r); u != nil {
		req.Name = u.Name()
	}
	var picked []int64
	if id := queryInt(r, "room"); id != 0 {
		picked = append(picked, id)
	}
	s.requestForm(w, r, http.StatusOK, req, picked, nil)
}

// readRequest reads the form into req and returns the rooms picked, which
// must be among the bookable ones.
func (s *Server) readRequest(r *http.Request, req *store.Request) ([]int64, []string, error) {
	req.Name, req.Contact, req.Title = formStr(r, "name"), formStr(r, "contact"), formStr(r, "title")
	req.StartsOn, req.StartTime = formStr(r, "starts_on"), formStr(r, "start_time")
	req.EndsOn, req.EndTime = formStr(r, "ends_on"), formStr(r, "end_time")
	req.Details = formStr(r, "details")
	if req.EndsOn == req.StartsOn {
		req.EndsOn = ""
	}
	var errs []string
	if req.Name == "" || len(req.Name) > 200 {
		errs = append(errs, "Tell us your name.")
	}
	if req.Contact == "" || len(req.Contact) > 200 {
		errs = append(errs, "Give an email address or phone number so we can reach you.")
	}
	if req.Title == "" || len(req.Title) > 200 {
		errs = append(errs, "Say what the room is for.")
	}
	if len(req.Details) > 4000 {
		errs = append(errs, "Keep the details under 4,000 characters.")
	}
	if v := formStr(r, "expected_count"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 100000 {
			errs = append(errs, "How many people is a number.")
		}
		req.ExpectedCount = n
	}
	when := checkWhen(req.StartsOn, req.StartTime, req.EndsOn, req.EndTime)
	if len(when) == 0 && req.StartsOn < s.today() {
		when = append(when, "Pick a day from today on.")
	}
	errs = append(errs, when...)

	bookable, err := s.bookableRooms()
	if err != nil {
		return nil, nil, err
	}
	var rooms []int64
	for _, v := range r.PostForm["room"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || slices.Contains(rooms, id) {
			continue
		}
		if slices.ContainsFunc(bookable, func(rm store.Room) bool { return rm.ID == id }) {
			rooms = append(rooms, id)
		}
	}
	if len(rooms) == 0 {
		errs = append(errs, "Pick at least one room.")
	}
	return rooms, errs, nil
}

// bookNow reports whether a member's request can be booked straight away:
// none of its rooms need approval and they're all free then.
func (s *Server) bookNow(req *store.Request, rooms []int64) (bool, error) {
	all, err := s.bookableRooms()
	if err != nil {
		return false, err
	}
	for _, id := range rooms {
		i := slices.IndexFunc(all, func(rm store.Room) bool { return rm.ID == id })
		if i < 0 || all[i].NeedsApproval {
			return false, nil
		}
	}
	busy, err := s.store.BusyInRooms(rooms, req.StartsOn, req.StartTime, req.EndsOn, req.EndTime)
	return len(busy) == 0, err
}

func (s *Server) handleRequestCreate(w http.ResponseWriter, r *http.Request) {
	if !s.canRequest(r) {
		s.sendToLogin(w, r)
		return
	}
	u := currentUser(r)
	ip := s.clientIP(r)
	if u == nil {
		// The honeypot is a field people can't see; bots fill it in. They
		// get the same answer as a real request, so they don't learn.
		if formStr(r, "website") != "" {
			s.redirect(w, r, "/", "Thanks, your request was sent.")
			return
		}
	}
	var req store.Request
	rooms, errs, err := s.readRequest(r, &req)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if u == nil && errs == nil && !s.requestLimiter.allow(ip) {
		errs = append(errs, "Too many requests from here. Please wait a while and try again, or call the church office.")
		s.requestForm(w, r, http.StatusTooManyRequests, req, rooms, errs)
		return
	}
	if errs != nil {
		s.requestForm(w, r, http.StatusUnprocessableEntity, req, rooms, errs)
		return
	}
	if u != nil {
		req.UserID = u.ID
	} else {
		s.requestLimiter.fail(ip) // counts sent requests, not failures
	}
	if err := s.store.CreateRequest(&req, rooms); err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := "Your request was sent. Keep the link to this page to check on it."
	if u != nil {
		now, err := s.bookNow(&req, rooms)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if now {
			if _, err := s.store.ApproveRequest(req.ID, 0, "Booked straight away: these rooms don't need approval.", "private"); err != nil {
				s.serverError(w, r, err)
				return
			}
			msg = "Booked. These rooms don't need approval, and they were free."
		}
	}
	s.redirect(w, r, requestURL(req.ID, req.Code), msg)
}

// availability is the JSON the request form's availability controller
// reads: for each bookable room, whether it's free at the times picked
// and, if not, when it's busy. Busy times only: no titles, whoever asks.
type availability struct {
	Ready bool                  `json:"ready"` // false until the times make sense
	Rooms map[string]roomStatus `json:"rooms"`
}

type roomStatus struct {
	Free bool     `json:"free"`
	Busy []string `json:"busy,omitempty"`
}

func (s *Server) handleRequestAvailability(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	if !s.canRequest(r) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "sign in to ask for a room"})
		return
	}
	q := r.URL.Query()
	startsOn, startTime, endsOn, endTime := q.Get("starts_on"), q.Get("start_time"), q.Get("ends_on"), q.Get("end_time")
	if endsOn == startsOn {
		endsOn = ""
	}
	out := availability{Rooms: map[string]roomStatus{}}
	if len(checkWhen(startsOn, startTime, endsOn, endTime)) > 0 {
		json.NewEncoder(w).Encode(out)
		return
	}
	rooms, err := s.bookableRooms()
	if err != nil {
		s.log.Error("availability", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	ids := make([]int64, len(rooms))
	for i, rm := range rooms {
		ids[i] = rm.ID
	}
	busy, err := s.store.BusyInRooms(ids, startsOn, startTime, endsOn, endTime)
	if err != nil {
		s.log.Error("availability", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	out.Ready = true
	for _, id := range ids {
		st := roomStatus{Free: len(busy[id]) == 0}
		for _, b := range busy[id] {
			when := b.When()
			if endsOn == "" && b.LastDate() == b.StartsOn {
				when = b.ShortWhen()
			}
			st.Busy = append(st.Busy, when)
		}
		out.Rooms[strconv.FormatInt(id, 10)] = st
	}
	json.NewEncoder(w).Encode(out)
}

// ownRequest loads the request in the path for the person who asked: they
// need its code (from the link), or to be the member who sent it. Schedulers
// may look too. Anyone else gets 404.
func (s *Server) ownRequest(w http.ResponseWriter, r *http.Request, code string) (*store.Request, bool) {
	u := currentUser(r)
	req, err := s.store.GetRequest(pathID(r))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, err)
		return nil, false
	}
	ok := req != nil && (subtle.ConstantTimeCompare([]byte(code), []byte(req.Code)) == 1 ||
		(u != nil && (u.ID == req.UserID || u.CanSchedule())))
	if !ok {
		s.render(w, r, http.StatusNotFound, "error", map[string]any{
			"Title": "Not found", "Message": "We couldn't find that request. Check the link you were given.", "PublicHeader": u == nil,
		})
		return nil, false
	}
	return req, true
}

func (s *Server) handleRequestStatus(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	req, ok := s.ownRequest(w, r, code)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	s.render(w, r, http.StatusOK, "requests/status", map[string]any{
		"Title": req.Title, "Request": req, "Code": req.Code, "PublicHeader": currentUser(r) == nil,
		"Link": s.baseURL(r) + requestURL(req.ID, req.Code),
	})
}

func (s *Server) handleRequestWithdraw(w http.ResponseWriter, r *http.Request) {
	req, ok := s.ownRequest(w, r, r.PostFormValue("code"))
	if !ok {
		return
	}
	if err := s.store.WithdrawRequest(req.ID); errors.Is(err, store.ErrAlreadyDecided) {
		s.setFlash(w, r, "error", "That request has already been answered, so it can't be withdrawn.")
		http.Redirect(w, r, requestURL(req.ID, req.Code), http.StatusSeeOther)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, requestURL(req.ID, req.Code), "Request withdrawn.")
}

// Schedulers ---------------------------------------------------------------

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if !slices.Contains(store.RequestStatuses, status) {
		status = "waiting"
	}
	list, err := s.store.ListRequests(status)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	clashes := map[int64]bool{}
	if status == "waiting" {
		for _, req := range list {
			busy, err := s.store.BusyInRooms(req.RoomIDs(), req.StartsOn, req.StartTime, req.EndsOn, req.EndTime)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			clashes[req.ID] = len(busy) > 0
		}
	}
	s.render(w, r, http.StatusOK, "requests/index", map[string]any{
		"Title": "Room requests", "Requests": list, "Status": status, "Statuses": store.RequestStatuses, "Clashes": clashes,
	})
}

func (s *Server) handleRequestShow(w http.ResponseWriter, r *http.Request) {
	req, err := s.store.GetRequest(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.requestPage(w, r, http.StatusOK, req, nil)
}

// requestPage is a request as schedulers see it: who asked, what clashes
// in its rooms, and the forms to answer it.
func (s *Server) requestPage(w http.ResponseWriter, r *http.Request, status int, req *store.Request, errs []string) {
	data := map[string]any{"Title": req.Title, "Request": req, "Errors": errs}
	if req.Waiting() {
		busy, err := s.store.BusyInRooms(req.RoomIDs(), req.StartsOn, req.StartTime, req.EndsOn, req.EndTime)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Busy"] = busy
	}
	rooms, err := s.store.ListRooms(true)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	capacity := map[int64]int{}
	for _, rm := range rooms {
		capacity[rm.ID] = rm.Capacity
	}
	data["Capacity"] = capacity
	if req.BookingID != 0 && s.EventsURL() != "" {
		if b, err := s.store.GetBooking(req.BookingID); err == nil {
			data["MakeEventURL"] = s.makeEventURL(b)
		}
	}
	if req.UserID != 0 {
		if asker, err := s.store.GetUser(req.UserID); err == nil {
			data["Asker"] = asker
		}
	}
	s.render(w, r, status, "requests/show", data)
}

// decided answers a scheduler who tried to answer a request already
// answered (by someone else, or a second click).
func (s *Server) decided(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusConflict, "error", map[string]any{
		"Title": "Already answered", "Message": "Someone has already answered that request. Go back to Requests to see what was decided.",
	})
}

// answerTooLong shows the request again when the note back is too long.
func (s *Server) answerTooLong(w http.ResponseWriter, r *http.Request, answer string) bool {
	if len(answer) <= 4000 {
		return false
	}
	req, err := s.store.GetRequest(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return true
	}
	s.requestPage(w, r, http.StatusUnprocessableEntity, req, []string{"Keep the note under 4,000 characters."})
	return true
}

func (s *Server) handleRequestApprove(w http.ResponseWriter, r *http.Request) {
	answer := formStr(r, "answer")
	if s.answerTooLong(w, r, answer) {
		return
	}
	bookingID, err := s.store.ApproveRequest(pathID(r), currentUser(r).ID, answer, formStr(r, "visibility"))
	if errors.Is(err, store.ErrAlreadyDecided) {
		s.decided(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	b, err := s.store.GetBooking(bookingID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	note, err := s.clashNote(b)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, bookingURL(bookingID), "Approved: "+b.Title+" is booked."+note)
}

func (s *Server) handleRequestDecline(w http.ResponseWriter, r *http.Request) {
	answer := formStr(r, "answer")
	if s.answerTooLong(w, r, answer) {
		return
	}
	err := s.store.DeclineRequest(pathID(r), currentUser(r).ID, answer)
	if errors.Is(err, store.ErrAlreadyDecided) {
		s.decided(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/requests", "Request turned down.")
}
