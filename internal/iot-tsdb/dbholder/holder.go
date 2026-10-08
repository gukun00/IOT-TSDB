// Package dbholder provides a hot-swappable reference to a *tsdb.DB.
// All consumers (custom API, web handler, promql engine) share one Holder so
// that a backup-restore can atomically switch the underlying database without
// restarting the process.
package dbholder

import (
	"context"
	"sync"

	"github.com/prometheus/prometheus/model/exemplar"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/metadata"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb"
)

// Holder guards the current *tsdb.DB and its stats. Every accessor resolves
// the db at call time, never caching the pointer, so a Set() mid-flight only
// affects subsequent calls.
type Holder struct {
	mu    sync.RWMutex
	db    *tsdb.DB
	stats *tsdb.DBStats

	// draining, when true, causes Guard to fail; used during restore.
	draining bool
}

// New creates a Holder around db.
func New(db *tsdb.DB, stats *tsdb.DBStats) *Holder {
	return &Holder{db: db, stats: stats}
}

// Get returns the current db. Callers must not retain it across a restore.
func (h *Holder) Get() *tsdb.DB {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.db
}

// Set swaps in a new db. The old db is NOT closed here; the restore flow owns
// the old db lifecycle.
func (h *Holder) Set(db *tsdb.DB, stats *tsdb.DBStats) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.db = db
	h.stats = stats
}

// StatsRef returns the current *tsdb.DBStats (used for WALReplayStatus).
func (h *Holder) StatsRef() *tsdb.DBStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stats
}

// SetDraining marks the holder as draining (restore in progress). While
// draining, Storage methods return tsdb.ErrNotReady.
func (h *Holder) SetDraining(v bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = v
}

func (h *Holder) get() (*tsdb.DB, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.draining || h.db == nil {
		return nil, tsdb.ErrNotReady
	}
	return h.db, nil
}

// StartTime implements storage.Storage.
func (h *Holder) StartTime() (int64, error) {
	db, err := h.get()
	if err != nil {
		return 0, err
	}
	return db.StartTime()
}

// Close implements storage.Storage. The process lifecycle owns the db; Close
// here is a no-op passthrough to keep the interface satisfied.
func (h *Holder) Close() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.db == nil {
		return nil
	}
	return h.db.Close()
}

// Appender implements storage.Storage.
func (h *Holder) Appender(ctx context.Context) storage.Appender {
	db, err := h.get()
	if err != nil {
		return notReadyAppender{err: err}
	}
	return db.Appender(ctx)
}

// Querier implements storage.Storage.
func (h *Holder) Querier(ctx context.Context, mint, maxt int64) (storage.Querier, error) {
	db, err := h.get()
	if err != nil {
		return nil, err
	}
	return db.Querier(ctx, mint, maxt)
}

// ChunkQuerier implements storage.Storage.
func (h *Holder) ChunkQuerier(ctx context.Context, mint, maxt int64) (storage.ChunkQuerier, error) {
	db, err := h.get()
	if err != nil {
		return nil, err
	}
	return db.ChunkQuerier(ctx, mint, maxt)
}

// ExemplarQuerier implements storage.ExemplarQueryable.
func (h *Holder) ExemplarQuerier(ctx context.Context) (storage.ExemplarQuerier, error) {
	db, err := h.get()
	if err != nil {
		return nil, err
	}
	return db.ExemplarQuerier(ctx)
}

// CleanTombstones implements api_v1.TSDBAdminStats.
func (h *Holder) CleanTombstones() error {
	db, err := h.get()
	if err != nil {
		return err
	}
	return db.CleanTombstones()
}

// Delete implements api_v1.TSDBAdminStats.
func (h *Holder) Delete(mint, maxt int64, ms ...*labels.Matcher) error {
	db, err := h.get()
	if err != nil {
		return err
	}
	return db.Delete(mint, maxt, ms...)
}

// Snapshot implements api_v1.TSDBAdminStats.
func (h *Holder) Snapshot(dir string, withHead bool) error {
	db, err := h.get()
	if err != nil {
		return err
	}
	return db.Snapshot(dir, withHead)
}

// Stats implements api_v1.TSDBAdminStats.
func (h *Holder) Stats(statsByLabelName string, limit int) (*tsdb.Stats, error) {
	db, err := h.get()
	if err != nil {
		return nil, err
	}
	return db.Head().Stats(statsByLabelName, limit), nil
}

// WALReplayStatus implements api_v1.TSDBAdminStats.
func (h *Holder) WALReplayStatus() (tsdb.WALReplayStatus, error) {
	stats := h.StatsRef()
	if stats == nil {
		return tsdb.WALReplayStatus{}, tsdb.ErrNotReady
	}
	return stats.Head.WALReplayStatus.GetWALReplayStatus(), nil
}

// notReadyAppender fails all operations with the stored error.
type notReadyAppender struct{ err error }

func (a notReadyAppender) Append(storage.SeriesRef, labels.Labels, int64, float64) (storage.SeriesRef, error) {
	return 0, a.err
}
func (a notReadyAppender) AppendExemplar(storage.SeriesRef, labels.Labels, exemplar.Exemplar) (storage.SeriesRef, error) {
	return 0, a.err
}
func (a notReadyAppender) AppendHistogram(storage.SeriesRef, labels.Labels, int64, *histogram.Histogram, *histogram.FloatHistogram) (storage.SeriesRef, error) {
	return 0, a.err
}
func (a notReadyAppender) UpdateMetadata(storage.SeriesRef, labels.Labels, metadata.Metadata) (storage.SeriesRef, error) {
	return 0, a.err
}
func (a notReadyAppender) Commit() error   { return a.err }
func (a notReadyAppender) Rollback() error { return a.err }
