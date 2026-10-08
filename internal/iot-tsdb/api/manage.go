package api

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/prometheus/internal/iot-tsdb/backup"
	"github.com/prometheus/prometheus/internal/iot-tsdb/dbholder"
	"github.com/prometheus/prometheus/internal/iot-tsdb/meta"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
)

// ManageHandler serves the point-management API under /api/manage/.
type ManageHandler struct {
	h           *dbholder.Holder
	meta        *meta.Store
	backups     *backup.Manager
	dataDir     string
	backupDir   string
	retentionMs int64
}

// NewManageHandler creates the handler.
func NewManageHandler(h *dbholder.Holder, store *meta.Store, bm *backup.Manager, dataDir, backupDir string, retention time.Duration) *ManageHandler {
	return &ManageHandler{h: h, meta: store, backups: bm, dataDir: dataDir, backupDir: backupDir, retentionMs: int64(retention / time.Millisecond)}
}

// Routes returns the /api/manage/ sub-router (paths are relative).
func (m *ManageHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/points", m.points)
	mux.HandleFunc("/points/meta", m.putMeta)
	mux.HandleFunc("/points/last", m.pointsLast)
	mux.HandleFunc("/history", m.history)
	mux.HandleFunc("/dashboard", m.dashboard)
	mux.HandleFunc("/export", m.exportTable)
	mux.HandleFunc("/import", m.importTable)
	mux.HandleFunc("/backup", m.backupCreate)
	mux.HandleFunc("/backups", m.backupList)
	mux.HandleFunc("/backups/", m.backupByName)
	mux.HandleFunc("/restore", m.backupRestore)
	return mux
}

// pointDTO is one row of the points table.
type pointDTO struct {
	Key         string            `json:"key"`
	Metric      string            `json:"metric"`
	Labels      map[string]string `json:"labels"`
	Description string            `json:"description,omitempty"`
	Unit        string            `json:"unit,omitempty"`
	RangeMin    *float64          `json:"range_min,omitempty"`
	RangeMax    *float64          `json:"range_max,omitempty"`
	Group       string            `json:"group,omitempty"`
	Online      bool              `json:"online"` // series exists in tsdb
	LastT       int64             `json:"last_t,omitempty"`
	LastV       *float64          `json:"last_v,omitempty"`
}

func labelsToMap(lset labels.Labels) map[string]string {
	out := map[string]string{}
	lset.Range(func(l labels.Label) {
		if l.Name != labels.MetricName {
			out[l.Name] = l.Value
		}
	})
	return out
}

// points handles GET /points (list+search+page) and DELETE /points.
func (m *ManageHandler) points(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		m.listPoints(w, r)
	case http.MethodDelete:
		m.deletePoints(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// listPoints enumerates all tsdb series, merges metadata, applies
// search/group filters and pagination.
func (m *ManageHandler) listPoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.ToLower(q.Get("search"))
	group := q.Get("group")
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 500 {
		size = 50
	}

	now := time.Now().UnixMilli()
	items, err := m.collectPoints(r, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Merge in metadata-only points (pre-registered, no series yet).
	if q.Get("include_offline") != "false" {
		for key, mt := range m.meta.All() {
			if _, exists := items[key]; exists {
				continue
			}
			metric, pairs, err := meta.ParseKey(key)
			if err != nil {
				continue
			}
			lm := map[string]string{}
			for _, kv := range pairs {
				lm[kv[0]] = kv[1]
			}
			items[key] = &pointDTO{
				Key: key, Metric: metric, Labels: lm,
				Description: mt.Description, Unit: mt.Unit,
				RangeMin: mt.RangeMin, RangeMax: mt.RangeMax, Group: mt.Group,
			}
		}
	}

	// Filter.
	filtered := make([]*pointDTO, 0, len(items))
	for _, p := range items {
		if group != "" && p.Group != group {
			continue
		}
		if search != "" {
			hay := strings.ToLower(p.Key + " " + p.Description)
			if !strings.Contains(hay, search) {
				continue
			}
		}
		filtered = append(filtered, p)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Key < filtered[j].Key })

	total := len(filtered)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	writeJSON(w, map[string]interface{}{
		"total": total,
		"page":  page,
		"size":  size,
		"items": filtered[start:end],
	})
}

// collectPoints queries the tsdb for all series and their last sample.
func (m *ManageHandler) collectPoints(r *http.Request, now int64) (map[string]*pointDTO, error) {
	querier, err := m.h.Querier(r.Context(), 0, now)
	if err != nil {
		return nil, err
	}
	defer querier.Close()

	items := map[string]*pointDTO{}
	// Empty matcher ("","") is tsdb's AllPostings special case; a Select with
	// zero matchers returns nothing.
	allMatcher, _ := labels.NewMatcher(labels.MatchEqual, "", "")
	ss := querier.Select(false, nil, allMatcher)
	for ss.Next() {
		s := ss.At()
		lset := s.Labels()
		metric := lset.Get(labels.MetricName)
		key := meta.Key(metric, lset)
		p := &pointDTO{Key: key, Metric: metric, Labels: labelsToMap(lset), Online: true}
		var lastT int64 = math.MinInt64
		var lastV float64
		it := s.Iterator(nil)
		for it.Next() == chunkenc.ValFloat {
			t, v := it.At()
			if t > lastT {
				lastT, lastV = t, v
			}
		}
		// A series whose samples are all tombstoned stays in the head index
		// until compaction; treat it as deleted (its metadata, if any, still
		// shows as an offline/pre-registered point).
		if lastT == math.MinInt64 {
			continue
		}
		p.LastT = lastT
		p.LastV = &lastV
		if mt, ok := m.meta.Get(key); ok {
			p.Description = mt.Description
			p.Unit = mt.Unit
			p.RangeMin = mt.RangeMin
			p.RangeMax = mt.RangeMax
			p.Group = mt.Group
		}
		items[key] = p
	}
	return items, ss.Err()
}

// deletePoints removes data (and optionally metadata) for matching series.
func (m *ManageHandler) deletePoints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Matchers string `json:"matchers"` // same JSON as /query
		Start    int64  `json:"start"`
		End      int64  `json:"end"`
		DropMeta bool   `json:"drop_meta"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	ms, err := parseMatchers(req.Matchers)
	if err != nil {
		http.Error(w, "bad matchers: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Start == 0 && req.End == 0 {
		req.Start, req.End = math.MinInt64/2, math.MaxInt64/2 // avoid overflow inside tsdb
	}
	if err := m.h.Delete(req.Start, req.End, ms...); err != nil {
		http.Error(w, "delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := m.h.CleanTombstones(); err != nil {
		http.Error(w, "clean tombstones: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if req.DropMeta {
		// Remove metadata for points that match the label filter.
		now := time.Now().UnixMilli()
		items, err := m.collectPoints(r, now)
		if err == nil {
			_ = items // series already gone; fall through to key-based cleanup below
		}
		for key := range m.meta.All() {
			lset, err := meta.LabelsFromKey(key)
			if err != nil {
				continue
			}
			if matchersMatch(ms, lset) {
				_ = m.meta.Delete(key)
			}
		}
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func matchersMatch(ms []*labels.Matcher, lset labels.Labels) bool {
	for _, m := range ms {
		if !m.Matches(lset.Get(m.Name)) {
			return false
		}
	}
	return true
}

// putMeta handles PUT /points/meta — upsert one point's metadata.
func (m *ManageHandler) putMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Key         string   `json:"key"`
		Description string   `json:"description"`
		Unit        string   `json:"unit"`
		RangeMin    *float64 `json:"range_min"`
		RangeMax    *float64 `json:"range_max"`
		Group       string   `json:"group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, _, err := meta.ParseKey(req.Key); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	created, err := m.meta.Upsert(req.Key, meta.PointMeta{
		Description: req.Description, Unit: req.Unit,
		RangeMin: req.RangeMin, RangeMax: req.RangeMax, Group: req.Group,
	})
	if err != nil {
		http.Error(w, "save meta: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"status": "ok", "created": created})
}

// pointsLast handles GET /points/last?keys=a,b,c — last sample per key.
func (m *ManageHandler) pointsLast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw := r.URL.Query().Get("keys")
	if raw == "" {
		writeJSON(w, map[string]interface{}{})
		return
	}
	keys := strings.Split(raw, ",")
	type lastOut struct {
		T int64   `json:"t"`
		V float64 `json:"v"`
	}
	out := map[string]*lastOut{}

	now := time.Now().UnixMilli()
	querier, err := m.h.Querier(r.Context(), 0, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer querier.Close()

	for _, key := range keys {
		lset, err := meta.LabelsFromKey(key)
		if err != nil {
			continue
		}
		ms := make([]*labels.Matcher, 0, 3)
		lset.Range(func(l labels.Label) {
			if m2, err := labels.NewMatcher(labels.MatchEqual, l.Name, l.Value); err == nil {
				ms = append(ms, m2)
			}
		})
		ss := querier.Select(false, nil, ms...)
		for ss.Next() {
			s := ss.At()
			var lastT int64 = math.MinInt64
			var lastV float64
			it := s.Iterator(nil)
			for it.Next() == chunkenc.ValFloat {
				t, v := it.At()
				if t > lastT {
					lastT, lastV = t, v
				}
			}
			if lastT > math.MinInt64 {
				out[key] = &lastOut{T: lastT, V: lastV}
			}
		}
		if err := ss.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, out)
}

// history handles GET /history?keys=&start=&end=&step=&agg= — bucketed
// downsampling, capped at 2000 points per series.
func (m *ManageHandler) history(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	rawKeys := q.Get("keys")
	if rawKeys == "" {
		http.Error(w, "keys required", http.StatusBadRequest)
		return
	}
	start, err1 := strconv.ParseInt(q.Get("start"), 10, 64)
	end, err2 := strconv.ParseInt(q.Get("end"), 10, 64)
	if err1 != nil || err2 != nil || end <= start {
		http.Error(w, "bad start/end (ms)", http.StatusBadRequest)
		return
	}
	step, _ := strconv.ParseInt(q.Get("step"), 10, 64)
	agg := q.Get("agg")
	if agg == "" {
		agg = "raw"
	}
	if agg != "raw" && agg != "avg" && agg != "max" && agg != "min" {
		http.Error(w, "agg must be raw|avg|max|min", http.StatusBadRequest)
		return
	}
	const maxPoints = 2000
	minStep := (end - start) / maxPoints
	if minStep < 1 {
		minStep = 1
	}
	if step < minStep {
		step = minStep
	}
	if agg == "raw" && (end-start)/step > maxPoints {
		step = (end-start)/maxPoints + 1
	}

	type seriesOut struct {
		Key    string   `json:"key"`
		Points [][2]any `json:"points"` // [[t, v], ...] (any to keep int64/float64 mixed JSON)
	}
	resp := struct {
		Step   int64       `json:"step"`
		Series []seriesOut `json:"series"`
	}{Step: step}

	querier, err := m.h.Querier(r.Context(), start, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer querier.Close()

	for _, key := range strings.Split(rawKeys, ",") {
		lset, err := meta.LabelsFromKey(key)
		if err != nil {
			continue
		}
		var ms []*labels.Matcher
		lset.Range(func(l labels.Label) {
			if m2, err := labels.NewMatcher(labels.MatchEqual, l.Name, l.Value); err == nil {
				ms = append(ms, m2)
			}
		})
		so := seriesOut{Key: key}
		ss := querier.Select(false, nil, ms...)
		for ss.Next() {
			s := ss.At()
			var bucketT int64 = math.MinInt64
			var vals []float64
			flush := func() {
				if bucketT == math.MinInt64 || len(vals) == 0 {
					return
				}
				var v float64
				switch agg {
				case "raw", "avg":
					v = aggAvg(vals)
				case "max":
					v = aggMax(vals)
				case "min":
					v = aggMin(vals)
				}
				so.Points = append(so.Points, [2]any{bucketT, v})
			}
			it := s.Iterator(nil)
			for it.Next() == chunkenc.ValFloat {
				t, v := it.At()
				bt := (t / step) * step
				if agg == "raw" {
					so.Points = append(so.Points, [2]any{t, v})
					continue
				}
				if bt != bucketT {
					flush()
					bucketT = bt
					vals = vals[:0]
				}
				vals = append(vals, v)
			}
			flush()
		}
		if err := ss.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Series = append(resp.Series, so)
	}
	writeJSON(w, resp)
}

// dashboard handles GET /dashboard — aggregate overview stats.
func (m *ManageHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	db := m.h.Get()
	groupCounts := map[string]int{}
	for _, mt := range m.meta.All() {
		g := mt.Group
		if g == "" {
			g = "(未分组)"
		}
		groupCounts[g]++
	}
	out := map[string]interface{}{
		"retention_ms": m.retentionMs,
		"meta_count":   m.meta.Len(),
		"groups":       m.meta.Groups(),
		"group_counts": groupCounts,
	}
	if db != nil {
		head := db.Head()
		out["series_count"] = head.NumSeries()
		out["head_stats"] = head.Stats("", 0)
		var size int64
		filepath.Walk(m.dataDir, func(_ string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				size += info.Size()
			}
			return nil
		})
		out["db_size_bytes"] = size
		oldest := int64(math.MaxInt64)
		for _, b := range db.Blocks() {
			if b.Meta().MinTime < oldest {
				oldest = b.Meta().MinTime
			}
		}
		if oldest == int64(math.MaxInt64) {
			oldest = 0
		}
		out["oldest_block_t"] = oldest
	}
	// Backup count: number of *.zip in backupDir.
	if entries, err := os.ReadDir(m.backupDir); err == nil {
		n := 0
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".zip") {
				n++
			}
		}
		out["backups_count"] = n
	}
	writeJSON(w, out)
}
