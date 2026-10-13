package server

import (
	"net/http"

	"github.com/tylergoza/church-calendar/internal/store"
)

// API tokens (admin): read tokens for sites and phones, and source tokens
// for the apps that push their bookings.

// tokenForm is what was typed into one of the two "make a token" forms,
// kept when the form comes back with errors.
type tokenForm struct {
	Kind, Name, Source string
	Private            bool
	Errors             []string
}

type appSource struct{ ID, Label string }

func appSources() []appSource {
	out := make([]appSource, len(store.AppSources))
	for i, src := range store.AppSources {
		out[i] = appSource{src, sourceLabel(src)}
	}
	return out
}

func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, status int, form tokenForm, newToken, newKind string) {
	read, err := s.store.ListTokens(store.TokenRead)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	apps, err := s.store.ListTokens(store.TokenSource)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	readForm, app := tokenForm{Kind: store.TokenRead}, tokenForm{Kind: store.TokenSource, Private: true}
	if form.Kind == store.TokenSource {
		app = form
	} else if form.Kind == store.TokenRead {
		readForm = form
	}
	base := s.baseURL(r)
	s.render(w, r, status, "tokens/index", map[string]any{
		"Title": "API tokens", "Tokens": read, "AppTokens": apps, "ReadForm": readForm, "AppForm": app,
		"Sources": appSources(), "NewToken": newToken, "NewKind": newKind,
		"APIBase": base + "/api/v1", "CalendarURL": base + "/calendar.ics",
	})
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	s.renderTokens(w, r, http.StatusOK, tokenForm{}, "", "")
}

// handleTokenCreate makes a token and shows it on the page, the one time it
// can be seen; it isn't put in a flash message or redirect.
func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	f := tokenForm{Kind: store.TokenRead, Name: formStr(r, "name"), Private: r.PostFormValue("see_private") != ""}
	if r.PostFormValue("kind") == store.TokenSource {
		f.Kind, f.Source = store.TokenSource, r.PostFormValue("source")
		if !store.IsAppSource(f.Source) {
			f.Errors = append(f.Errors, "Pick the app that will send its events.")
		}
	}
	if f.Name == "" {
		f.Errors = append(f.Errors, "Name what will use it, like Church website.")
	}
	if len(f.Name) > 100 {
		f.Errors = append(f.Errors, "Keep the name under 100 characters.")
	}
	if f.Errors != nil {
		s.renderTokens(w, r, http.StatusUnprocessableEntity, f, "", "")
		return
	}
	var token string
	var err error
	if f.Kind == store.TokenSource {
		token, err = s.store.CreateSourceToken(f.Name, f.Source, f.Private, currentUser(r).ID)
	} else {
		token, err = s.store.CreateReadToken(f.Name, f.Private, currentUser(r).ID)
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderTokens(w, r, http.StatusOK, tokenForm{}, token, f.Kind)
}

func (s *Server) handleTokenPrivate(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SetTokenPrivate(pathID(r), r.PostFormValue("private") == "1"); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/admin/tokens", "Token updated.")
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteToken(pathID(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/admin/tokens", "Token revoked. Anything using it can no longer reach the calendar.")
}
