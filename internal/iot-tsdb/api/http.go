// Package api provides the HTTP interface for IOT-TSDB.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/prometheus/prometheus/internal/iot-tsdb/dbholder"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
)

// Handler wraps a swappable db holder and exposes HTTP endpoints.
type Handler struct {
	h *dbholder.Holder
}

// NewHandler creates a Handler for the given holder.
func NewHandler(h *dbholder.Holder) *Handler {
	return &Handler{h: h}
}

// Routes returns an http.Handler with all API routes registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/write", h.write)
	mux.HandleFunc("/query", h.query)
	mux.HandleFunc("/query_agg", h.queryAgg)
	mux.HandleFunc("/series", h.series)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	return mux
}

type writeRequest struct {
	Samples []sampleDTO `json:"samples"`
}

type sampleDTO struct {
	Metric    string            `json:"metric"`
	Labels    map[string]string `json:"labels"`
	Timestamp int64             `json:"timestamp"`
	Value     float64           `json:"value"`
}

func (h *Handler) write(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req writeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	app := h.h.Appender(r.Context())
	for _, s := range req.Samples {
		lb := labels.NewBuilder(labels.EmptyLabels())
		for k, v := range s.Labels {
			lb.Set(k, v)
		}
		if s.Metric != "" {
			lb.Set(labels.MetricName, s.Metric)
		}
		lset := lb.Labels()
		if _, err := app.Append(0, lset, s.Timestamp, s.Value); err != nil {
			app.Rollback()
			http.Error(w, "append: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := app.Commit(); err != nil {
		http.Error(w, "commit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type matcherDTO struct {
	Name  string `json:"name"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

func parseMatchers(raw string) ([]*labels.Matcher, error) {
	var dtos []matcherDTO
	if err := json.Unmarshal([]byte(raw), &dtos); err != nil {
		return nil, err
	}
	out := make([]*labels.Matcher, 0, len(dtos))
	for _, d := range dtos {
		var mt labels.MatchType
		switch d.Op {
		case "=":
			mt = labels.MatchEqual
		case "!=":
			mt = labels.MatchNotEqual
		case "=~":
			mt = labels.MatchRegexp
		case "!~":
			mt = labels.MatchNotRegexp
		default:
			return nil, fmt.Errorf("unknown op %q", d.Op)
		}
		m, err := labels.NewMatcher(mt, d.Name, d.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func queryParams(r *http.Request) (matchers []*labels.Matcher, mint, maxt int64, err error) {
	matchers, err = parseMatchers(r.URL.Query().Get("matchers"))
	if err != nil {
		return nil, 0, 0, err
	}
	mint, err = strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
	if err != nil {
		mint = 0
	}
	maxt, err = strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
	if err != nil {
		maxt = 1<<62 - 1
	}
	return matchers, mint, maxt, nil
}

type sampleOut struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

type seriesOut struct {
	Labels  []labels.Label `json:"labels"`
	Samples []sampleOut    `json:"samples"`
}

func (h *Handler) query(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	matchers, mint, maxt, err := queryParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q, err := h.h.Querier(r.Context(), mint, maxt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer q.Close()

	ss := q.Select(false, nil, matchers...)
	var out []seriesOut
	for ss.Next() {
		s := ss.At()
		so := seriesOut{Labels: labelsToSlice(s.Labels())}
		it := s.Iterator(nil)
		for it.Next() == chunkenc.ValFloat {
			t, v := it.At()
			so.Samples = append(so.Samples, sampleOut{T: t, V: v})
		}
		out = append(out, so)
	}
	if err := ss.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (h *Handler) queryAgg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	matchers, mint, maxt, err := queryParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	agg := r.URL.Query().Get("agg")
	var fn func([]float64) float64
	switch agg {
	case "sum":
		fn = aggSum
	case "avg":
		fn = aggAvg
	case "max":
		fn = aggMax
	case "min":
		fn = aggMin
	default:
		http.Error(w, "agg must be sum|avg|max|min", http.StatusBadRequest)
		return
	}

	q, err := h.h.Querier(r.Context(), mint, maxt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer q.Close()

	type aggResult struct {
		Labels []labels.Label `json:"labels"`
		Value  float64        `json:"value"`
	}
	var out []aggResult
	ss := q.Select(false, nil, matchers...)
	for ss.Next() {
		s := ss.At()
		var vals []float64
		it := s.Iterator(nil)
		for it.Next() == chunkenc.ValFloat {
			_, v := it.At()
			vals = append(vals, v)
		}
		out = append(out, aggResult{Labels: labelsToSlice(s.Labels()), Value: fn(vals)})
	}
	if err := ss.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (h *Handler) series(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	matchers, mint, maxt, err := queryParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q, err := h.h.Querier(r.Context(), mint, maxt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer q.Close()

	ss := q.Select(false, nil, matchers...)
	var out [][]labels.Label
	for ss.Next() {
		out = append(out, labelsToSlice(ss.At().Labels()))
	}
	if err := ss.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func labelsToSlice(lset labels.Labels) []labels.Label {
	out := make([]labels.Label, 0, lset.Len())
	lset.Range(func(l labels.Label) {
		out = append(out, l)
	})
	return out
}

func aggSum(vals []float64) float64 {
	var s float64
	for _, v := range vals {
		s += v
	}
	return s
}

func aggAvg(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	return aggSum(vals) / float64(len(vals))
}

func aggMax(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func aggMin(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
