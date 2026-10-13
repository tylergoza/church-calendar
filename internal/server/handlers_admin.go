package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Settings & backup ------------------------------------------------------

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "settings", map[string]any{"Title": "Settings", "SiteNameValue": s.SiteName(), "PublicRequests": s.PublicRequests()})
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "site_name")
	if name == "" {
		s.render(w, r, http.StatusUnprocessableEntity, "settings", map[string]any{
			"Title": "Settings", "Errors": []string{"Site name is required."}, "SiteNameValue": name,
			"PublicRequests": r.PostFormValue("public_requests") == "1",
		})
		return
	}
	public := ""
	if r.PostFormValue("public_requests") == "1" {
		public = "1"
	}
	for k, v := range map[string]string{"site_name": name, publicRequestsSetting: public} {
		if err := s.store.SetSetting(k, v); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.reloadSettings()
	s.redirect(w, r, "/admin/settings", "Settings saved.")
}

// handleBackup streams a consistent snapshot of the database. Restoring is
// just replacing the .db file on the target host while the app is stopped.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "cal-backup-")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "backup.db")
	if err := s.store.Backup(r.Context(), dest); err != nil {
		s.serverError(w, r, err)
		return
	}
	f, err := os.Open(dest)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer f.Close()
	name := "calendar-" + time.Now().Format("2006-01-02-1504") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	io.Copy(w, f)
}
