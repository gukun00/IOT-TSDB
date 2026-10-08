// Command iot-tsdb is a time-series database server based on the Prometheus
// v2.45 LTS tsdb kernel, exposing the native Prometheus web UI plus a minimal
// HTTP write/query API.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	kitlog "github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"

	"github.com/prometheus/prometheus/internal/iot-tsdb/api"
	"github.com/prometheus/prometheus/internal/iot-tsdb/backup"
	"github.com/prometheus/prometheus/internal/iot-tsdb/dbholder"
	"github.com/prometheus/prometheus/internal/iot-tsdb/meta"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/web"
)

func main() {
	dataDir := flag.String("data-dir", "./data", "tsdb data directory")
	listen := flag.String("listen", ":8080", "HTTP listen address")
	retention := flag.Duration("retention", 15*24*time.Hour, "data retention duration")
	blockRange := flag.Duration("block-range", 2*time.Hour, "head block time range")
	pageTitle := flag.String("page-title", "IOT-TSDB", "web UI page title")
	backupDir := flag.String("backup-dir", "", "backup directory (default: <data-dir>/../backups)")
	metaPath := flag.String("meta-file", "", "point metadata file (default: <data-dir>/../pointmeta.json)")
	flag.Parse()

	if *backupDir == "" {
		*backupDir = filepath.Join(filepath.Dir(*dataDir), "backups")
	}
	if *metaPath == "" {
		*metaPath = filepath.Join(filepath.Dir(*dataDir), "pointmeta.json")
	}

	logger := kitlog.NewLogfmtLogger(os.Stdout)

	opts := tsdb.DefaultOptions()
	opts.RetentionDuration = int64(*retention / time.Millisecond)
	opts.MinBlockDuration = int64(*blockRange / time.Millisecond)
	opts.MaxBlockDuration = int64(*blockRange / time.Millisecond)

	stats := tsdb.NewDBStats()
	db, err := tsdb.Open(*dataDir, logger, prometheus.DefaultRegisterer, opts, stats)
	if err != nil {
		log.Fatalf("open tsdb: %v", err)
	}
	defer db.Close()

	holder := dbholder.New(db, stats)

	metaStore, err := meta.Open(*metaPath)
	if err != nil {
		log.Fatalf("open pointmeta: %v", err)
	}

	backupMgr, err := backup.NewManager(holder, metaStore, *dataDir, *backupDir, *metaPath, opts, kitlog.With(logger, "component", "backup"))
	if err != nil {
		log.Fatalf("init backup manager: %v", err)
	}

	// PromQL engine backed by the local tsdb.
	queryEngine := promql.NewEngine(promql.EngineOpts{
		Logger:               kitlog.With(logger, "component", "query engine"),
		Reg:                  prometheus.DefaultRegisterer,
		MaxSamples:           50000000,
		Timeout:              2 * time.Minute,
		LookbackDelta:        5 * time.Minute,
		EnableAtModifier:     true,
		EnableNegativeOffset: true,
	})

	externalURL, _ := url.Parse("http://localhost" + *listen)

	webOpts := web.Options{
		Context:               context.Background(),
		TSDBDir:               *dataDir,
		TSDBRetentionDuration: model.Duration(*retention),
		LocalStorage:          holder,
		Storage:               holder,
		ExemplarStorage:       holder,
		QueryEngine:           queryEngine,
		LookbackDelta:         5 * time.Minute,
		ListenAddress:         *listen,
		ExternalURL:           externalURL,
		RoutePrefix:           "/",
		UseLocalAssets:        true,
		PageTitle:             *pageTitle,
		Flags:                 map[string]string{},
		Registerer:            prometheus.DefaultRegisterer,
		Gatherer:              prometheus.DefaultGatherer,
	}

	webHandler := web.New(kitlog.With(logger, "component", "web"), &webOpts)
	webHandler.SetReady(true)

	// Combine the native web handler with our custom write/query API.
	mux := http.NewServeMux()
	customAPI := api.NewHandler(holder).Routes()
	mux.Handle("/write", customAPI)
	mux.Handle("/query", customAPI)
	mux.Handle("/query_agg", customAPI)
	mux.Handle("/series", customAPI)
	mux.Handle("/health", customAPI)
	manageAPI := api.NewManageHandler(holder, metaStore, backupMgr, *dataDir, *backupDir, *retention).Routes()
	mux.Handle("/api/manage/", http.StripPrefix("/api/manage", manageAPI))
	mux.Handle("/manage/", http.StripPrefix("/manage/", http.FileServer(http.Dir("web/manageui"))))
	mux.Handle("/", webHandler)

	srv := &http.Server{Addr: *listen, Handler: mux}

	go func() {
		log.Printf("iot-tsdb listening on %s (UI at http://localhost%s)", *listen, *listen)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
