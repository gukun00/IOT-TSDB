package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/prometheus/internal/iot-tsdb/meta"
)

// pointTableRow is one import/export row.
type pointTableRow struct {
	Key         string   `json:"key"`
	Metric      string   `json:"metric"`
	Labels      string   `json:"labels"` // "k=v;k=v" sorted
	Description string   `json:"description,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	RangeMin    *float64 `json:"range_min,omitempty"`
	RangeMax    *float64 `json:"range_max,omitempty"`
	Group       string   `json:"group,omitempty"`
}

var csvHeader = []string{"metric", "labels", "description", "unit", "range_min", "range_max", "group"}

// exportTable handles GET /export?format=csv|json — full point table
// (metadata entries + discovered series).
func (m *ManageHandler) exportTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}

	// Base: all metadata entries.
	rows := map[string]*pointTableRow{}
	for key, mt := range m.meta.All() {
		metric, pairs, err := meta.ParseKey(key)
		if err != nil {
			continue
		}
		rows[key] = &pointTableRow{
			Key: key, Metric: metric, Labels: encodePairs(pairs),
			Description: mt.Description, Unit: mt.Unit,
			RangeMin: mt.RangeMin, RangeMax: mt.RangeMax, Group: mt.Group,
		}
	}
	// Add discovered series without metadata.
	now := time.Now().UnixMilli()
	if items, err := m.collectPoints(r, now); err == nil {
		for key, p := range items {
			if _, ok := rows[key]; ok {
				continue
			}
			pairs := make([][2]string, 0, len(p.Labels))
			names := make([]string, 0, len(p.Labels))
			for n := range p.Labels {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				pairs = append(pairs, [2]string{n, p.Labels[n]})
			}
			rows[key] = &pointTableRow{Key: key, Metric: p.Metric, Labels: encodePairs(pairs)}
		}
	}

	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	switch format {
	case "json":
		out := make([]*pointTableRow, 0, len(keys))
		for _, k := range keys {
			out = append(out, rows[k])
		}
		w.Header().Set("Content-Disposition", `attachment; filename="pointtable.json"`)
		writeJSON(w, map[string]interface{}{"points": out})
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="pointtable.csv"`)
		// UTF-8 BOM so Excel opens Chinese text correctly.
		w.Write([]byte{0xEF, 0xBB, 0xBF})
		cw := csv.NewWriter(w)
		cw.Write(csvHeader)
		for _, k := range keys {
			row := rows[k]
			cw.Write([]string{
				row.Metric, row.Labels, row.Description, row.Unit,
				floatPtrToStr(row.RangeMin), floatPtrToStr(row.RangeMax), row.Group,
			})
		}
		cw.Flush()
	default:
		http.Error(w, "format must be csv|json", http.StatusBadRequest)
	}
}

func encodePairs(pairs [][2]string) string {
	var b strings.Builder
	for i, kv := range pairs {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(kv[0])
		b.WriteByte('=')
		b.WriteString(kv[1])
	}
	return b.String()
}

func floatPtrToStr(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'g', -1, 64)
}

// importTable handles POST /import (multipart file field "file", or raw body
// with ?format=csv|json). Upserts metadata rows.
func (m *ManageHandler) importTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var data []byte
	var err error
	format := r.URL.Query().Get("format")

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "parse multipart: "+err.Error(), http.StatusBadRequest)
			return
		}
		f, fh, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file field required: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer f.Close()
		data, err = io.ReadAll(f)
		if err != nil {
			http.Error(w, "read file: "+err.Error(), http.StatusBadRequest)
			return
		}
		if format == "" {
			if strings.HasSuffix(strings.ToLower(fh.Filename), ".json") {
				format = "json"
			} else {
				format = "csv"
			}
		}
	} else {
		data, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if format == "" {
			format = "json"
		}
	}

	var rows []pointTableRow
	var parseErrs []string
	switch format {
	case "json":
		var payload struct {
			Points []pointTableRow `json:"points"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		rows = payload.Points
	case "csv":
		rows, parseErrs = parseCSVTable(data)
	default:
		http.Error(w, "format must be csv|json", http.StatusBadRequest)
		return
	}

	created, updated := 0, 0
	var errs []string
	errs = append(errs, parseErrs...)
	for i, row := range rows {
		key, err := row.key()
		if err != nil {
			errs = append(errs, fmt.Sprintf("row %d: %v", i+1, err))
			continue
		}
		c, err := m.meta.Upsert(key, meta.PointMeta{
			Description: row.Description, Unit: row.Unit,
			RangeMin: row.RangeMin, RangeMax: row.RangeMax, Group: row.Group,
		})
		if err != nil {
			errs = append(errs, fmt.Sprintf("row %d (%s): %v", i+1, key, err))
			continue
		}
		if c {
			created++
		} else {
			updated++
		}
	}
	writeJSON(w, map[string]interface{}{
		"created": created,
		"updated": updated,
		"errors":  errs,
	})
}

// key computes the canonical point key from a row (re-sorts label pairs).
func (row *pointTableRow) key() (string, error) {
	if row.Metric == "" {
		return "", fmt.Errorf("metric required")
	}
	var pairs [][2]string
	if row.Labels != "" {
		for _, p := range strings.Split(row.Labels, ";") {
			kv := strings.SplitN(p, "=", 2)
			if len(kv) != 2 || kv[0] == "" {
				return "", fmt.Errorf("bad label pair %q", p)
			}
			pairs = append(pairs, [2]string{kv[0], kv[1]})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	var b strings.Builder
	b.WriteString(row.Metric)
	for _, kv := range pairs {
		b.WriteByte('|')
		b.WriteString(kv[0])
		b.WriteByte('=')
		b.WriteString(kv[1])
	}
	return b.String(), nil
}

func parseCSVTable(data []byte) ([]pointTableRow, []string) {
	// Strip UTF-8 BOM if present.
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	cr := csv.NewReader(strings.NewReader(text))
	cr.FieldsPerRecord = -1
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, []string{"csv parse: " + err.Error()}
	}
	var rows []pointTableRow
	var errs []string
	for i, rec := range recs {
		if i == 0 && len(rec) > 0 && strings.EqualFold(rec[0], "metric") {
			continue // header
		}
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}
		if len(rec) < 2 {
			errs = append(errs, fmt.Sprintf("row %d: need at least metric,labels", i+1))
			continue
		}
		row := pointTableRow{
			Metric: strings.TrimSpace(rec[0]),
			Labels: strings.TrimSpace(rec[1]),
		}
		if len(rec) > 2 {
			row.Description = rec[2]
		}
		if len(rec) > 3 {
			row.Unit = rec[3]
		}
		if len(rec) > 4 {
			row.RangeMin = parseFloatPtr(rec[4])
		}
		if len(rec) > 5 {
			row.RangeMax = parseFloatPtr(rec[5])
		}
		if len(rec) > 6 {
			row.Group = rec[6]
		}
		rows = append(rows, row)
	}
	return rows, errs
}

func parseFloatPtr(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}
