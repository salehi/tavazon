// Package web serves the embedded dashboard and the JSON control/metrics API.
// It is pure stdlib net/http; the dashboard is a single self-contained file
// embedded with //go:embed. See docs/project.md §10, §7.13.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "embed"

	"tavazon/internal/config"
	"tavazon/internal/geoip"
	"tavazon/internal/metering"
	"tavazon/internal/metrics"
	"tavazon/internal/netstat"
	"tavazon/internal/state"
)

//go:embed static/index.html
var indexHTML []byte

// tehran is the billing/curve time zone; it falls back to UTC if the embedded
// zoneinfo is unavailable.
var tehran = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// ConfigHolder is the RWMutex-guarded current config shared by the engine
// (which reads it through Get every cycle) and the web layer (which replaces it
// on PUT). The config object is never mutated in place — a write swaps in a new
// pointer — so a reader's snapshot stays consistent for the whole cycle.
type ConfigHolder struct {
	mu   sync.RWMutex
	cfg  *config.Config
	path string
}

// NewConfigHolder wraps cfg, persisting future writes to path.
func NewConfigHolder(cfg *config.Config, path string) *ConfigHolder {
	return &ConfigHolder{cfg: cfg, path: path}
}

// Get returns the current config pointer. Callers must treat it as read-only.
func (h *ConfigHolder) Get() *config.Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}

// Replace swaps in next as the current config and persists it to disk.
func (h *ConfigHolder) Replace(next *config.Config) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg = next
	return writeJSONFile(h.path, h.cfg)
}

// Update applies fn to a copy of the current config, swaps the copy in, and
// persists it. The live config object is never mutated.
func (h *ConfigHolder) Update(fn func(*config.Config)) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := *h.cfg
	fn(&next)
	h.cfg = &next
	return writeJSONFile(h.path, h.cfg)
}

// Server is the dashboard HTTP server. geoip may be nil when the GeoIP
// databases failed to load — the ASN endpoint then returns an empty list.
type Server struct {
	cfg       *ConfigHolder
	state     *state.State
	metrics   *metrics.Registry
	metering  *metering.Store
	geoip     *geoip.GeoIP
	netstat   func(iface string) (netstat.Counters, error) // injectable for tests
	startTime time.Time
	log       *slog.Logger
}

// New assembles a Server from its wired dependencies.
func New(cfg *ConfigHolder, st *state.State, m *metrics.Registry, store *metering.Store, g *geoip.GeoIP, log *slog.Logger) *Server {
	return &Server{
		cfg:       cfg,
		state:     st,
		metrics:   m,
		metering:  store,
		geoip:     g,
		netstat:   netstat.Read,
		startTime: time.Now(),
		log:       log,
	}
}

// Handler builds the routed http.Handler: the §10.1 routes behind a panic
// backstop and the bearer-token auth middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/asns", s.handleASNs)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/billing", s.handleBilling)
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("POST /api/control/start", s.handleStart)
	mux.HandleFunc("POST /api/control/stop", s.handleStop)
	mux.HandleFunc("POST /api/control/dev", s.handleDev)
	mux.HandleFunc("POST /api/control/reset-counters", s.handleResetCounters)
	mux.HandleFunc("POST /api/control/set-sync", s.handleSetSync)
	return s.recoverMW(s.authMW(mux))
}

// Run serves Handler on cfg.Web.Listen until ctx is cancelled, then shuts the
// server down gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:    s.cfg.Get().Web.Listen,
		Handler: s.Handler(),
	}
	errc := make(chan error, 1)
	go func() {
		s.log.Info("dashboard listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// recoverMW turns a panic in any handler into a 500 instead of crashing the
// process (docs/RED_LINES X4).
func (s *Server) recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("web handler panic recovered", "panic", rec, "path", r.URL.Path)
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// authMW enforces the configured auth token on /api/* and /metrics. The token
// arrives as "Authorization: Bearer <t>" or "?token=<t>". /healthz and the
// dashboard page are always open.
func (s *Server) authMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.cfg.Get().Web.AuthToken
		if token != "" && requiresAuth(r.URL.Path) {
			if presentedToken(r) != token {
				writeErr(w, http.StatusUnauthorized, "missing or invalid auth token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requiresAuth(path string) bool {
	return strings.HasPrefix(path, "/api/") || path == "/metrics"
}

func presentedToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

// --- handlers --------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	body := s.metrics.Prometheus()
	body += fmt.Sprintf("tavazon_running %d\n", boolToInt(cfg.General.Running))
	body += fmt.Sprintf("tavazon_dev_mode %d\n", boolToInt(cfg.Dev.Enabled))
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Write([]byte(body))
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	snap := s.metrics.Snapshot()

	rawUp := s.state.TotalUpload - s.state.UploadSync
	rawDown := s.state.TotalDownload - s.state.DownloadSync
	var ratio float64
	if s.state.TotalDownload > 0 {
		ratio = float64(s.state.TotalUpload) / float64(s.state.TotalDownload)
	}

	resp := map[string]any{
		"uptime_seconds":   int64(time.Since(s.startTime).Seconds()),
		"running":          cfg.General.Running,
		"mode":             cfg.Target.Mode,
		"dev":              map[string]any{"enabled": cfg.Dev.Enabled, "target": cfg.Dev.Target},
		"tracked":          map[string]int64{"upload": s.state.TotalUpload, "download": s.state.TotalDownload},
		"raw":              map[string]int64{"upload": rawUp, "download": rawDown},
		"sync":             map[string]int64{"upload": s.state.UploadSync, "download": s.state.DownloadSync},
		"speed":            map[string]int64{"upload_bps": snap.UploadBPS, "download_bps": snap.DownloadBPS},
		"ratio":            map[string]float64{"current": ratio, "target": cfg.Target.Ratio.Multiplier},
		"deficit_bytes":    snap.DeficitBytes,
		"workers_active":   snap.WorkersActive,
		"curve_intensity":  snap.CurveIntensity,
		"resources":        snap.Resources,
		"fake_bytes_cycle": snap.FakeBytesCycle,
		"fake_bytes_total": snap.FakeBytesTotal,
		"per_asn_cycle":    s.recentPerASN(),
		"tehran_time":      time.Now().In(tehran).Format(time.RFC3339),
		"speed_samples":    snap.SpeedSamples,
	}
	writeJSON(w, http.StatusOK, resp)
}

// recentPerASN returns the per-ASN fake-byte split of the most recent metering
// bucket, the closest available proxy for "this cycle".
func (s *Server) recentPerASN() map[uint32]int64 {
	now := time.Now()
	buckets, err := s.metering.History(now.Add(-10*time.Minute), now)
	if err != nil || len(buckets) == 0 {
		return map[uint32]int64{}
	}
	return buckets[len(buckets)-1].PerASN
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Get())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	old := s.cfg.Get()
	next := *old // decode the body over a copy of the current config
	if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if _, err := next.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid config: "+err.Error())
		return
	}
	diff := diffConfig(old, &next)
	if err := s.cfg.Replace(&next); err != nil {
		writeErr(w, http.StatusInternalServerError, "persist config: "+err.Error())
		return
	}
	if diff != "" {
		s.audit("api", diff)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "change": diff})
}

func (s *Server) handleASNs(w http.ResponseWriter, r *http.Request) {
	if s.geoip == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	country := r.URL.Query().Get("country")
	infos := s.geoip.ListASNs(country)
	out := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		out = append(out, map[string]any{
			"number":  info.Number,
			"name":    info.Name,
			"country": info.Country,
			"num_ips": info.NumIPs,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	to := parseTime(q.Get("to"), time.Now())
	from := parseTime(q.Get("from"), to.Add(-24*time.Hour))
	buckets, err := s.metering.History(from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "history: "+err.Error())
		return
	}
	if buckets == nil {
		buckets = []metering.Bucket{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":    from.Format(time.RFC3339),
		"to":      to.Format(time.RFC3339),
		"buckets": buckets,
	})
}

func (s *Server) handleBilling(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	now := time.Now()
	from := billingWindowStart(now, cfg.Metering.BillingWindow)
	b, err := s.metering.Billing(from, now, cfg.Metering.Percentile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "billing: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	n := parseInt(r.URL.Query().Get("n"), 100)
	records, err := s.metering.Audit(n)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "audit: "+err.Error())
		return
	}
	if records == nil {
		records = []metering.AuditRecord{}
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := parseInt(r.URL.Query().Get("n"), 200)
	lines := tailLines(s.cfg.Get().Log.File, n)
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.setRunning(w, true)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.setRunning(w, false)
}

func (s *Server) setRunning(w http.ResponseWriter, running bool) {
	was := s.cfg.Get().General.Running
	if err := s.cfg.Update(func(c *config.Config) { c.General.Running = running }); err != nil {
		writeErr(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	s.audit("api", fmt.Sprintf("general.running: %v → %v", was, running))
	writeJSON(w, http.StatusOK, map[string]any{"running": running})
}

func (s *Server) handleDev(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	was := s.cfg.Get().Dev.Enabled
	if err := s.cfg.Update(func(c *config.Config) { c.Dev.Enabled = body.Enabled }); err != nil {
		writeErr(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	s.audit("api", fmt.Sprintf("dev.enabled: %v → %v", was, body.Enabled))
	writeJSON(w, http.StatusOK, map[string]any{"enabled": body.Enabled})
}

func (s *Server) handleResetCounters(w http.ResponseWriter, r *http.Request) {
	raw, err := s.netstat(s.cfg.Get().Network.Interface)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "netstat: "+err.Error())
		return
	}
	// tracked = raw + sync; anchoring sync to -raw makes the tracked total 0.
	s.state.TotalUpload = 0
	s.state.TotalDownload = 0
	s.state.UploadSync = -int64(raw.TxBytes)
	s.state.DownloadSync = -int64(raw.RxBytes)
	if err := s.state.Save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "save state: "+err.Error())
		return
	}
	s.log.Warn("counters reset to zero from dashboard")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetSync(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var body struct {
		UploadGB   float64 `json:"upload_gb"`
		DownloadGB float64 `json:"download_gb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.UploadGB < 0 || body.DownloadGB < 0 {
		writeErr(w, http.StatusBadRequest, "totals must not be negative")
		return
	}
	raw, err := s.netstat(s.cfg.Get().Network.Interface)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "netstat: "+err.Error())
		return
	}
	const giB = 1 << 30
	s.state.TotalUpload = int64(body.UploadGB * giB)
	s.state.TotalDownload = int64(body.DownloadGB * giB)
	s.state.UploadSync = s.state.TotalUpload - int64(raw.TxBytes)
	s.state.DownloadSync = s.state.TotalDownload - int64(raw.RxBytes)
	if err := s.state.Save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "save state: "+err.Error())
		return
	}
	s.log.Warn("lifetime totals set from dashboard",
		"upload_gb", body.UploadGB, "download_gb", body.DownloadGB)
	writeJSON(w, http.StatusOK, map[string]any{
		"tracked_upload":   s.state.TotalUpload,
		"tracked_download": s.state.TotalDownload,
	})
}

// audit appends a config-change record, logging (not failing) on a write error.
func (s *Server) audit(source, change string) {
	if err := s.metering.AppendAudit(metering.AuditRecord{Source: source, Change: change}); err != nil {
		s.log.Error("audit write failed", "err", err)
	}
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseInt(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// parseTime accepts an RFC3339 timestamp or a Unix-seconds integer.
func parseTime(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0)
	}
	return def
}

// billingWindowStart returns the start of the current billing window.
func billingWindowStart(now time.Time, window string) time.Time {
	now = now.In(tehran)
	switch window {
	case "day":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tehran)
	case "week":
		return now.AddDate(0, 0, -7)
	default: // "month"
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, tehran)
	}
}

// writeJSONFile marshals v and writes it atomically: temp file, then rename.
func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("web: marshal %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("web: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("web: rename %s: %w", tmp, err)
	}
	return nil
}

// tailLines returns the last n lines of the file at path, oldest first.
func tailLines(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil || n <= 0 {
		return []string{}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// diffConfig renders a field-level diff of two configs as "path: old → new"
// clauses joined by "; ". It returns "" when the configs are equal.
func diffConfig(old, next *config.Config) string {
	om, nm := toMap(old), toMap(next)
	var out []string
	diffMaps("", om, nm, &out)
	sort.Strings(out)
	return strings.Join(out, "; ")
}

func toMap(v any) map[string]any {
	data, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(data, &m)
	return m
}

func diffMaps(prefix string, old, next map[string]any, out *[]string) {
	for k, nv := range next {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		ov, ok := old[k]
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: (unset) → %v", path, nv))
			continue
		}
		oc, ocok := ov.(map[string]any)
		nc, ncok := nv.(map[string]any)
		if ocok && ncok {
			diffMaps(path, oc, nc, out)
			continue
		}
		if !reflect.DeepEqual(ov, nv) {
			*out = append(*out, fmt.Sprintf("%s: %v → %v", path, ov, nv))
		}
	}
}
