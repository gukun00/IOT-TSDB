package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/prometheus/prometheus/internal/iot-tsdb/backup"
)

// backupCreate handles POST /backup {name?}.
func (m *ManageHandler) backupCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	info, err := m.backups.Create(req.Name)
	if err != nil {
		http.Error(w, "backup: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, info)
}

// backupList handles GET /backups.
func (m *ManageHandler) backupList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	infos, err := m.backups.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if infos == nil {
		infos = []backup.Info{}
	}
	writeJSON(w, map[string]interface{}{"backups": infos})
}

// backupByName handles GET /backups/{name}/download and DELETE /backups/{name}.
// Go 1.21 ServeMux has no path params; parse manually.
func (m *ManageHandler) backupByName(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/backups/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		m.backupList(w, r)
		return
	}
	var name string
	isDownload := false
	if strings.HasSuffix(rest, "/download") {
		name = strings.TrimSuffix(rest, "/download")
		isDownload = true
	} else {
		name = rest
	}

	switch {
	case isDownload && r.Method == http.MethodGet:
		p, err := m.backups.Path(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
		w.Header().Set("Content-Type", "application/zip")
		http.ServeFile(w, r, p)
	case !isDownload && r.Method == http.MethodDelete:
		if err := m.backups.Delete(name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// backupRestore handles POST /restore — multipart file field "file" (upload)
// or JSON {"name": "..."} (existing backup).
func (m *ManageHandler) backupRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var name string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(8 << 30); err != nil {
			http.Error(w, "parse multipart: "+err.Error(), http.StatusBadRequest)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file field required: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer f.Close()
		name, err = m.backups.SaveUploaded(f)
		if err != nil {
			http.Error(w, "save upload: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "json {\"name\": \"...\"} required", http.StatusBadRequest)
			return
		}
		name = req.Name
	}

	if err := m.backups.Restore(name); err != nil {
		http.Error(w, "restore: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok", "restored_from": name})
}
