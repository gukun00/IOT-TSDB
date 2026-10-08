// Package backup implements snapshot-based backup (zip) and in-process
// restore (hot swap) for IOT-TSDB.
package backup

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	kitlog "github.com/go-kit/log"
	"github.com/go-kit/log/level"

	"github.com/prometheus/prometheus/internal/iot-tsdb/dbholder"
	"github.com/prometheus/prometheus/internal/iot-tsdb/meta"
	"github.com/prometheus/prometheus/tsdb"
)

// Manager orchestrates backup creation and restore. Only one operation runs
// at a time.
type Manager struct {
	mu        sync.Mutex
	h         *dbholder.Holder
	meta      *meta.Store
	dataDir   string
	backupDir string
	metaPath  string
	opts      *tsdb.Options
	logger    kitlog.Logger
}

// Info describes one backup archive.
type Info struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created int64  `json:"created"` // ms
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

// NewManager creates the manager, ensuring backupDir exists.
func NewManager(h *dbholder.Holder, store *meta.Store, dataDir, backupDir, metaPath string, opts *tsdb.Options, logger kitlog.Logger) (*Manager, error) {
	if err := os.MkdirAll(backupDir, 0o777); err != nil {
		return nil, err
	}
	return &Manager{
		h: h, meta: store,
		dataDir: dataDir, backupDir: backupDir, metaPath: metaPath,
		opts: opts, logger: logger,
	}, nil
}

// BackupDir returns the configured backup directory.
func (m *Manager) BackupDir() string { return m.backupDir }

// Create snapshots the current db and packs it (plus pointmeta.json) into
// backups/<name>.zip. Empty name defaults to backup-<yyyymmdd-HHMMSS>.
func (m *Manager) Create(name string) (*Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if name == "" {
		name = "backup-" + time.Now().Format("20060102-150405")
	}
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid backup name %q", name)
	}

	tmp := filepath.Join(m.backupDir, ".snap-"+name)
	os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)

	db := m.h.Get()
	if db == nil {
		return nil, fmt.Errorf("database not ready")
	}
	if err := db.Snapshot(tmp, true); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	// Attach a copy of the point metadata.
	if data, err := os.ReadFile(m.metaPath); err == nil {
		if err := os.WriteFile(filepath.Join(tmp, "pointmeta.json"), data, 0o666); err != nil {
			return nil, fmt.Errorf("attach pointmeta: %w", err)
		}
	}

	zipPath := filepath.Join(m.backupDir, name+".zip")
	os.Remove(zipPath)
	if err := zipDir(tmp, zipPath); err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	fi, err := os.Stat(zipPath)
	if err != nil {
		return nil, err
	}
	level.Info(m.logger).Log("msg", "backup created", "name", name, "size", fi.Size())
	return &Info{Name: name, Size: fi.Size(), Created: fi.ModTime().UnixMilli()}, nil
}

// List returns all backups sorted by name descending (newest name first).
func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{
			Name:    strings.TrimSuffix(e.Name(), ".zip"),
			Size:    fi.Size(),
			Created: fi.ModTime().UnixMilli(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// Path returns the zip path for a backup name, validating it exists.
func (m *Manager) Path(name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("invalid backup name %q", name)
	}
	p := filepath.Join(m.backupDir, name+".zip")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("backup %q not found", name)
	}
	return p, nil
}

// Delete removes a backup archive.
func (m *Manager) Delete(name string) error {
	p, err := m.Path(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// SaveUploaded stores an uploaded archive as a regular backup and returns its
// generated name.
func (m *Manager) SaveUploaded(r io.Reader) (string, error) {
	name := "upload-" + time.Now().Format("20060102-150405")
	p := filepath.Join(m.backupDir, name+".zip")
	f, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		os.Remove(p)
		return "", err
	}
	return name, nil
}

// Restore replaces the live database with the contents of backup <name>,
// hot-swapping the holder. On failure it attempts to roll back to the
// previous database directory; the returned error reports both causes.
func (m *Manager) Restore(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	zipPath, err := m.Path(name)
	if err != nil {
		return err
	}
	ts := time.Now().Format("20060102-150405")
	staging := filepath.Join(m.backupDir, ".restore-"+ts)
	os.RemoveAll(staging)
	defer os.RemoveAll(staging)

	if err := unzip(zipPath, staging); err != nil {
		return fmt.Errorf("unzip: %w", err)
	}

	// Validate: at least one block dir containing meta.json.
	blocks, err := findBlocks(staging)
	if err != nil || len(blocks) == 0 {
		return fmt.Errorf("not a valid IOT-TSDB backup (no tsdb blocks found)")
	}

	// Read bundled pointmeta before the staging dir moves.
	var restoredMeta map[string]meta.PointMeta
	haveMeta := false
	if data, err := os.ReadFile(filepath.Join(staging, "pointmeta.json")); err == nil {
		var mm map[string]meta.PointMeta
		if json.Unmarshal(data, &mm) == nil {
			restoredMeta, haveMeta = mm, true
		}
	}

	// Precheck: the snapshot must open cleanly. nil registerer avoids
	// duplicate metric registration (guarded in tsdb).
	preDB, err := tsdb.Open(staging, m.logger, nil, m.opts, tsdb.NewDBStats())
	if err != nil {
		return fmt.Errorf("precheck open: %w", err)
	}
	preDB.Close()

	// Swap.
	m.h.SetDraining(true)
	old := m.h.Get()
	if old != nil {
		old.Close()
	}

	bakDir := m.dataDir + ".bak-" + ts
	os.RemoveAll(bakDir)
	if err := os.Rename(m.dataDir, bakDir); err != nil {
		m.h.SetDraining(false)
		return fmt.Errorf("rename data dir aside: %w (old db closed, restart required)", err)
	}
	stagedData := filepath.Join(m.backupDir, ".restore-"+ts+"-data")
	os.RemoveAll(stagedData)
	if err := os.Rename(staging, stagedData); err != nil {
		m.rollback(bakDir, "")
		return fmt.Errorf("move staging: %w", err)
	}
	if err := os.Rename(stagedData, m.dataDir); err != nil {
		m.rollback(bakDir, "")
		return fmt.Errorf("move staging into place: %w", err)
	}

	newStats := tsdb.NewDBStats()
	newDB, err := tsdb.Open(m.dataDir, m.logger, nil, m.opts, newStats)
	if err != nil {
		m.rollback(bakDir, stagedData)
		return fmt.Errorf("open restored db (rolled back): %w", err)
	}
	m.h.Set(newDB, newStats)
	m.h.SetDraining(false)

	if haveMeta {
		if err := m.meta.ReplaceAll(restoredMeta); err != nil {
			level.Error(m.logger).Log("msg", "restore pointmeta failed (data restored)", "err", err)
		}
	}
	level.Info(m.logger).Log("msg", "restore completed", "backup", name, "blocks", len(blocks))
	return nil
}

// rollback renames bakDir back into place and reopens the old database.
func (m *Manager) rollback(bakDir, stagedData string) {
	if stagedData != "" {
		os.RemoveAll(stagedData)
	}
	os.RemoveAll(m.dataDir)
	if err := os.Rename(bakDir, m.dataDir); err != nil {
		level.Error(m.logger).Log("msg", "rollback rename failed", "err", err)
		m.h.SetDraining(false)
		return
	}
	stats := tsdb.NewDBStats()
	db, err := tsdb.Open(m.dataDir, m.logger, nil, m.opts, stats)
	if err != nil {
		level.Error(m.logger).Log("msg", "rollback reopen failed", "err", err)
		m.h.SetDraining(false)
		return
	}
	m.h.Set(db, stats)
	m.h.SetDraining(false)
	level.Info(m.logger).Log("msg", "rolled back to previous database")
}

// findBlocks returns subdirectories of dir that look like tsdb blocks.
func findBlocks(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "meta.json")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// zipDir packs the contents of src into dst (relative paths, dirs implied).
func zipDir(src, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		r, err := os.Open(path)
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(w, r)
		return err
	})
}

// unzip extracts the archive into dst, guarding against path traversal.
func unzip(zipPath, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("unsafe path in zip: %q", f.Name)
		}
		target := filepath.Join(dst, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o777); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(w, rc); err != nil {
			w.Close()
			rc.Close()
			return err
		}
		w.Close()
		rc.Close()
	}
	return nil
}
