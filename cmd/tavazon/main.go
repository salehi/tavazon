// Command tavazon manufactures balancing UDP traffic so an asymmetric hosting
// quota stays healthy. This file is wiring only — no business logic. See
// docs/project.md §7.1, §12.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"syscall"

	_ "time/tzdata" // embed zoneinfo (~450 KB) so Asia/Tehran resolves on scratch/distroless

	"tavazon/internal/config"
	"tavazon/internal/engine"
	"tavazon/internal/geoip"
	"tavazon/internal/logging"
	"tavazon/internal/metering"
	"tavazon/internal/metrics"
	"tavazon/internal/schedule"
	"tavazon/internal/state"
	"tavazon/internal/targets"
	"tavazon/internal/uploader"
	"tavazon/internal/web"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath := flag.String("config", "config.json", "path to config.json")
	flag.String("state", "", "override state file path")
	flag.String("asn-db", "", "override GeoLite2-ASN.mmdb path")
	flag.String("country-db", "", "override GeoLite2-Country.mmdb path")
	flag.String("listen", "", "override web listen address")
	flag.String("mode", "", "override target mode: ratio|volume")
	flag.Int("multiplier", 0, "override ratio multiplier")
	flag.Bool("stopped", false, "start in the stopped state (overrides general.running)")
	flag.Bool("no-web", false, "disable the web dashboard")
	flag.String("log-level", "", "debug|info|warn|error")
	printConfig := flag.Bool("print-config", false, "print the effective merged config as JSON and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("tavazon", version)
		return
	}

	// Precedence: defaults → config.json → TAVAZON_* env → CLI flags.
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("config: %v", err)
	}
	if err := cfg.ApplyEnv(); err != nil {
		fatal("config env: %v", err)
	}
	cfg.ApplyFlags(flag.CommandLine)
	warnings, err := cfg.Validate()
	if err != nil {
		fatal("config invalid: %v", err)
	}

	if *printConfig {
		out, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(out))
		return
	}

	log, closeLog, err := logging.Setup(cfg.Log)
	if err != nil {
		fatal("logging: %v", err)
	}
	defer closeLog()
	for _, w := range warnings {
		log.Warn("config warning", "detail", w)
	}

	st, err := state.Load(cfg.State.File)
	if err != nil {
		fatal("state: %v", err)
	}

	// GeoIP failure is not fatal: the dashboard must still come up so the
	// operator sees the problem; the uploader simply stays idle (no targets).
	var g *geoip.GeoIP
	if gi, err := geoip.Open(cfg.GeoIP.ASNDB, cfg.GeoIP.CountryDB); err != nil {
		log.Error("geoip missing — uploader idle until databases are provided",
			"err", err, "asn_db", cfg.GeoIP.ASNDB, "country_db", cfg.GeoIP.CountryDB)
	} else {
		g = gi
		defer g.Close()
		log.Info("geoip loaded", "asn_count", len(g.ListASNs("")))
	}

	selected := cfg.GeoIP.SelectedASNs
	if g == nil {
		selected = nil
	}
	tg, skipped, err := targets.New(g, selected, cfg.Targets, mkRNG())
	if err != nil {
		log.Error("targets: no selected ASN resolved — uploader idle", "err", err)
		tg, _, _ = targets.New(nil, nil, cfg.Targets, mkRNG())
	}
	if len(skipped) > 0 {
		log.Warn("some selected ASNs were not found in the database", "asns", skipped)
	}

	store, err := metering.Open(cfg.Metering)
	if err != nil {
		fatal("metering: %v", err)
	}

	reg := metrics.New()
	holder := web.NewConfigHolder(cfg, *configPath)
	up := uploader.New(tg, reg, store, holder.Get, log, mkRNG())
	curve := schedule.NewCurve(cfg.Curve, mkRNG())
	eng := engine.New(holder.Get, st, curve, up, store, reg, log, mkRNG())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	if cfg.Web.Enabled {
		srv := web.New(holder, st, reg, store, g, log)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Error("web server panic recovered", "panic", r)
				}
			}()
			if err := srv.Run(ctx); err != nil {
				log.Error("web server stopped", "err", err)
			}
		}()
	}

	if err := eng.Run(ctx); err != nil {
		log.Error("engine stopped", "err", err)
	}

	// Graceful shutdown: the engine has finished its cycle; flush metering,
	// persist state, wait for the web server, then exit 0.
	if err := store.Close(); err != nil {
		log.Error("metering flush failed", "err", err)
	}
	if err := st.Save(); err != nil {
		log.Error("final state save failed", "err", err)
	}
	wg.Wait()
	log.Info("shutdown complete")
}

// mkRNG returns a freshly seeded PRNG. Each component gets its own: a
// math/rand/v2 *Rand is not safe for concurrent use.
func mkRNG() *rand.Rand {
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tavazon: "+format+"\n", args...)
	os.Exit(1)
}
