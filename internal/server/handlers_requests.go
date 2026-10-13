package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tylergoza/church-calendar/internal/store"
)

// Room requests at /request. Signed-in members book: when every room is
// free it's booked (confirmed) straight away, otherwise it's refused with
// what's in the way. Visitors (when public requests are on, the default)
// ask: their request makes a pending booking at once, which blocks the
// rooms until a scheduler approves or turns it down at /requests. People
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

// roomGroup is a building on the request form with the bookable rooms in
// it. Whole is the building itself when it can be picked whole.
type roomGroup struct {
	Name  string
	Whole *store.Room
	Rooms []store.Room
}

// groupRooms sorts the bookable rooms under the top-level room they are in
// (a building, or deeper: a room in a floor-less area still lands under
// its building). Rooms in no building, and buildings with nothing inside,
// are loose. all is every room offered, in name order.
func groupRooms(all, bookable []store.Room) (loose []store.Room, groups []roomGroup) {
	byID := map[int64]store.Room{}
	for _, rm := range all {
		byID[rm.ID] = rm
	}
	top := func(rm store.Room) store.Room {
		for seen := 0; rm.ParentID != 0 && seen < len(all); seen++ {
			up, ok := byID[rm.ParentID]
			if !ok {
				break
			}
			rm = up
		}
		return rm
	}
	idx := map[int64]int{}
	for _, rm := range bookable {
		root := top(rm)
		if root.ID == rm.ID {
			continue
		}
		i, ok := idx[root.ID]
		if !ok {
			i = len(groups)
			idx[root.ID] = i
			g := roomGroup{Name: root.Name}
			if root.Bookable {
				whole := root
				g.Whole = &whole
			}
			groups = append(groups, g)
		}
		groups[i].Rooms = append(groups[i].Rooms, rm)
	}
	for _, rm := range bookable {
		if _, isGroup := idx[rm.ID]; !isGroup && top(rm).ID == rm.ID {
			loose = append(loose, rm)
		}
	}
	return loose, groups
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
	everyRoom, err := s.store.ListRooms(false)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	loose, groups := groupRooms(everyRoom, rooms)
	equip, err := s.formEquipmentRows(rooms, req.Equipment, req.StartsOn, req.StartTime, req.EndsOn, req.EndTime, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	u := currentUser(r)
	data := map[string]any{
		"Title": "Ask for a room", "Form": req, "Rooms": rooms, "Loose": loose, "Groups": groups, "Picked": isPicked, "Errors": errs, "Equip": equip,
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

// phoneDigits is the digits of a phone number once spaces, dashes, dots,
// brackets and a leading + are taken out; "" if anything else is in it or
// it isn't 7–15 digits long.
func phoneDigits(phone string) string {
	phone = strings.TrimPrefix(strings.TrimSpace(phone), "+")
	var b strings.Builder
	for _, c := range phone {
		switch {
		case c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == ' ' || c == '-' || c == '.' || c == '(' || c == ')':
		default:
			return ""
		}
	}
	if n := b.Len(); n < 7 || n > 15 {
		return ""
	}
	return b.String()
}

// looksLikeEmail is a loose check: something@something.something, no spaces.
func looksLikeEmail(v string) bool {
	at := strings.LastIndex(v, "@")
	return at > 0 && strings.Contains(v[at:], ".") && !strings.ContainsAny(v, " \t<>,") && !strings.HasSuffix(v, ".")
}

// readRequest reads the form into req and returns the rooms picked, which
// must be among the bookable ones. Visitors must give a phone number;
// signed-in people's accounts say who they are.
func (s *Server) readRequest(r *http.Request, req *store.Request) ([]int64, []string, error) {
	req.Equipment = nil
	visitor := currentUser(r) == nil
	req.Name, req.Contact, req.Title = formStr(r, "name"), formStr(r, "contact"), formStr(r, "title")
	req.Phone = formStr(r, "phone")
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
	switch {
	case req.Phone == "" && visitor:
		errs = append(errs, "Give a phone number so a scheduler can call or text you.")
	case req.Phone != "" && (len(req.Phone) > 40 || phoneDigits(req.Phone) == ""):
		errs = append(errs, "That phone number doesn't look right: 7 to 15 digits, like 555-123-4567.")
	}
	if req.Contact != "" && (len(req.Contact) > 200 || !looksLikeEmail(req.Contact)) {
		errs = append(errs, "That email address doesn't look right. It's optional: leave it empty if you like.")
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
	lines, equipErrs, err := s.readEquipment(r, rooms)
	if err != nil {
		return nil, nil, err
	}
	req.Equipment = lines
	return rooms, append(errs, equipErrs...), nil
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
		// Members book: free is booked, busy is refused and nothing saved.
		req.UserID = u.ID
		busy, err := s.store.BookRequest(&req, rooms)
		if errors.Is(err, store.ErrRoomsBusy) {
			s.requestForm(w, r, http.StatusConflict, req, rooms, busyErrors(busy, req.EndsOn != ""))
			return
		} else if err != nil {
			s.serverError(w, r, err)
			return
		}
		note, err := s.bookingShortNote(req.BookingID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.redirect(w, r, requestURL(req.ID, req.Code), "Booked. The rooms were free, so they're yours."+note)
		return
	}
	s.requestLimiter.fail(ip) // counts sent requests, not failures
	if err := s.store.CreateRequest(&req, rooms); err != nil {
		s.serverError(w, r, err)
		return
	}
	note, err := s.bookingShortNote(req.BookingID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, requestURL(req.ID, req.Code), "Your request was sent. Keep the link to this page to check on it."+note)
}

// busyErrors says what's in the way of a booking, one line per room: busy
// times only, never titles (as the live availability shows). overDays is
// set when the booking runs over several days, so times need their dates.
func busyErrors(busy map[int64][]store.Booking, overDays bool) []string {
	var out []string
	seen := map[string]bool{}
	for id, list := range busy {
		name := ""
		var times []string
		for _, b := range list {
			for _, rm := range b.Rooms {
				if rm.RoomID == id {
					name = rm.Name
				}
			}
			times = append(times, busyWhen(b, overDays))
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name+" is taken then: "+strings.Join(times, "; ")+".")
	}
	slices.Sort(out)
	return out
}

// busyWhen is when a busy booking is, for someone booking on one day (just
// the times) or over several (with dates).
func busyWhen(b store.Booking, overDays bool) string {
	when := b.When()
	if !overDays && b.LastDate() == b.StartsOn {
		when = b.ShortWhen()
	}
	if b.NotYet() {
		when += " (pending)"
	}
	return when
}

// availability is the JSON the request form's availability controller
// reads: for each bookable room, whether it's free at the times picked
// and, if not, when it's busy. Busy times only: no titles, whoever asks.
type availability struct {
	Ready bool                  `json:"ready"` // false until the times make sense
	Rooms map[string]roomStatus `json:"rooms"`
	// Equipment is how much of each counted piece is not yet asked for at
	// those times, by equipment ID. except=<booking ID> leaves that
	// booking out (the form that changes it).
	Equipment map[string]equipLeft `json:"equipment"`
}

type equipLeft struct {
	Total int `json:"total"`
	Left  int `json:"left"`
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
	out := availability{Rooms: map[string]roomStatus{}, Equipment: map[string]equipLeft{}}
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
			st.Busy = append(st.Busy, busyWhen(b, endsOn != ""))
		}
		out.Rooms[strconv.FormatInt(id, 10)] = st
	}
	asked, err := s.store.EquipmentAskedFor(startsOn, startTime, endsOn, endTime, queryInt(r, "except"))
	if err != nil {
		s.log.Error("availability", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	all, err := s.store.ListEquipment(false)
	if err != nil {
		s.log.Error("availability", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	for _, e := range all {
		if !e.Service() {
			out.Equipment[strconv.FormatInt(e.ID, 10)] = equipLeft{Total: e.Total, Left: max(0, e.Total-asked[e.ID])}
		}
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
	data := map[string]any{
		"Title": req.Title, "Request": req, "Code": req.Code, "PublicHeader": currentUser(r) == nil,
		"Link": s.baseURL(r) + requestURL(req.ID, req.Code),
	}
	// The member who asked may change or cancel it, unless a planner took
	// the booking over: then it's part of an event, and that's linked.
	if u := currentUser(r); u != nil && req.UserID != 0 && u.ID == req.UserID {
		b, taken, err := s.takenOver(req)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Owner"] = true
		data["CanChange"] = s.canChange(req, taken)
		if taken {
			data["EventBooking"] = b
		}
	}
	s.render(w, r, http.StatusOK, "requests/status", data)
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
	if status == "short" {
		s.handleShortBookings(w, r)
		return
	}
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
	// What it asks for: the booking's lines when it has one (they know how
	// short they are), the request's own otherwise.
	var lines []store.EquipmentLine
	var eb *store.Booking
	if req.BookingID != 0 {
		if eb, err = s.store.GetBooking(req.BookingID); err == nil {
			lines, err = s.store.BookingEquipment(eb.ID)
		}
	} else {
		lines, err = s.store.RequestEquipment(req.ID)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, err)
		return
	}
	if data["Equip"], err = s.buildEquipView(lines, eb, true, false, ""); err != nil {
		s.serverError(w, r, err)
		return
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
