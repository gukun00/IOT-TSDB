package meta

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
)

func TestKeyRoundTrip(t *testing.T) {
	lb := labels.NewBuilder(labels.EmptyLabels())
	lb.Set(labels.MetricName, "temp")
	lb.Set("site", "b")
	lb.Set("dev", "a1")
	key := Key("temp", lb.Labels())
	want := "temp|dev=a1|site=b"
	if key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}
	metric, pairs, err := ParseKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if metric != "temp" || len(pairs) != 2 || pairs[0] != [2]string{"dev", "a1"} || pairs[1] != [2]string{"site", "b"} {
		t.Fatalf("bad round trip: %v %v", metric, pairs)
	}
	lset, err := LabelsFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if got := lset.Get("dev"); got != "a1" {
		t.Fatalf("dev = %q", got)
	}
	if got := lset.Get(labels.MetricName); got != "temp" {
		t.Fatalf("__name__ = %q", got)
	}
}

func TestStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pointmeta.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	min, max := 0.0, 100.0
	created, err := s.Upsert("temp|dev=a1", PointMeta{Description: "炉温", Unit: "°C", RangeMin: &min, RangeMax: &max, Group: "kiln"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected created=true")
	}
	created, err = s.Upsert("temp|dev=a1", PointMeta{Description: "炉温2"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected created=false on update")
	}
	// Overwrite existing file (Windows rename-cover path).
	if _, err := s.Upsert("temp|dev=a1", PointMeta{Description: "炉温3"}); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := s2.Get("temp|dev=a1")
	if !ok || m.Description != "炉温3" {
		t.Fatalf("reloaded = %+v, ok=%v", m, ok)
	}
	if err := s2.Delete("temp|dev=a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get("temp|dev=a1"); ok {
		t.Fatal("expected deleted")
	}
	if err := s2.Delete("missing"); err != nil {
		t.Fatal("delete missing should be nil error")
	}
}

func TestStoreConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pointmeta.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := s.Upsert("m", PointMeta{Description: "d"}); err != nil {
					t.Error(err)
					return
				}
				s.Get("m")
			}
		}(i)
	}
	wg.Wait()
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1", s.Len())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
