package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"namizungo/internal/config"
	"namizungo/internal/geoip"
	"namizungo/internal/metering"
	"namizungo/internal/metrics"
	"namizungo/internal/netstat"
	"namizungo/internal/state"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestServer builds a Server backed by temp-dir state/metering/config and a
// fake netstat source, plus the httptest.Server fronting its Handler.
func newTestServer(t *testing.T, token string) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()

	cfg := config.Default()
	cfg.Web.AuthToken = token
	cfg.Log.File = filepath.Join(dir, "tavazon.log")
	if err := os.WriteFile(cfg.Log.File, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	holder := NewConfigHolder(cfg, filepath.Join(dir, "config.json"))

	st, err := state.Load(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := metering.Open(config.MeteringConfig{
		Dir:           filepath.Join(dir, "metering"),
		Retention5Min: config.Duration(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, ipnet, _ := net.ParseCIDR("203.0.113.0/24")
	g := geoip.NewForTest(
		map[uint32][]net.IPNet{123: {*ipnet}},
		map[uint32]string{123: "IR"},
	)

	s := New(holder, st, metrics.New(), store, g, discardLogger())
	s.netstat = func(string) (netstat.Counters, error) {
		return netstat.Counters{TxBytes: 5000, RxBytes: 9000}, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// get/do issue a request and return status + decoded body.
func req(t *testing.T, method, url, token string, body string) (int, []byte) {
	t.Helper()
	var r *http.Request
	var err error
	if body != "" {
		r, err = http.NewRequest(method, url, strings.NewReader(body))
	} else {
		r, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestRoutes(t *testing.T) {
	_, ts := newTestServer(t, "")
	cases := []struct {
		method, path string
		body         string
		wantJSON     bool
	}{
		{"GET", "/", "", false},
		{"GET", "/healthz", "", false},
		{"GET", "/metrics", "", false},
		{"GET", "/api/stats", "", true},
		{"GET", "/api/config", "", true},
		{"GET", "/api/asns?country=IR", "", true},
		{"GET", "/api/history", "", true},
		{"GET", "/api/billing", "", true},
		{"GET", "/api/audit?n=10", "", true},
		{"GET", "/api/logs?n=50", "", true},
		{"POST", "/api/control/start", "", true},
		{"POST", "/api/control/stop", "", true},
		{"POST", "/api/control/dev", `{"enabled":true}`, true},
		{"POST", "/api/control/reset-counters", "", true},
		{"POST", "/api/control/set-sync", `{"upload_gb":2,"download_gb":1}`, true},
	}
	for _, c := range cases {
		status, body := req(t, c.method, ts.URL+c.path, "", c.body)
		if status != http.StatusOK {
			t.Errorf("%s %s: status %d, want 200 (body %s)", c.method, c.path, status, body)
			continue
		}
		if c.wantJSON {
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				t.Errorf("%s %s: body is not JSON: %v", c.method, c.path, err)
			}
		}
	}
}

func TestStatsShape(t *testing.T) {
	_, ts := newTestServer(t, "")
	_, body := req(t, "GET", ts.URL+"/api/stats", "", "")
	var s map[string]any
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"uptime_seconds", "running", "mode", "dev", "tracked",
		"raw", "sync", "speed", "ratio", "deficit_bytes", "workers_active",
		"curve_intensity", "resources", "tehran_time", "speed_samples"} {
		if _, ok := s[k]; !ok {
			t.Errorf("/api/stats missing key %q", k)
		}
	}
}

func TestAuth(t *testing.T) {
	_, ts := newTestServer(t, "s3cret")

	// /healthz is open regardless of the token.
	if status, _ := req(t, "GET", ts.URL+"/healthz", "", ""); status != http.StatusOK {
		t.Errorf("/healthz without token: status %d, want 200", status)
	}
	// /api/* without a token is rejected.
	if status, _ := req(t, "GET", ts.URL+"/api/stats", "", ""); status != http.StatusUnauthorized {
		t.Errorf("/api/stats without token: status %d, want 401", status)
	}
	// Wrong token is rejected.
	if status, _ := req(t, "GET", ts.URL+"/api/stats", "wrong", ""); status != http.StatusUnauthorized {
		t.Errorf("/api/stats wrong token: status %d, want 401", status)
	}
	// Correct bearer token is accepted.
	if status, _ := req(t, "GET", ts.URL+"/api/stats", "s3cret", ""); status != http.StatusOK {
		t.Errorf("/api/stats with token: status %d, want 200", status)
	}
	// Query-parameter token is also accepted.
	if status, _ := req(t, "GET", ts.URL+"/api/stats?token=s3cret", "", ""); status != http.StatusOK {
		t.Errorf("/api/stats with ?token: status %d, want 200", status)
	}
}

func TestPutConfigRejectsBadInput(t *testing.T) {
	s, ts := newTestServer(t, "")

	before, err := s.metering.Audit(0)
	if err != nil {
		t.Fatal(err)
	}
	// multiplier 99 is outside the Validate-enforced [2,15] range.
	status, _ := req(t, "PUT", ts.URL+"/api/config", "",
		`{"target":{"ratio":{"multiplier":99}}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("bad PUT: status %d, want 400", status)
	}
	after, err := s.metering.Audit(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("a rejected PUT wrote %d audit records, want 0", len(after)-len(before))
	}
	// The live config must be unchanged.
	if s.cfg.Get().Target.Ratio.Multiplier == 99 {
		t.Error("a rejected PUT mutated the live config")
	}
}

func TestPutConfigAppliesPersistsAudits(t *testing.T) {
	s, ts := newTestServer(t, "")

	status, body := req(t, "PUT", ts.URL+"/api/config", "",
		`{"target":{"ratio":{"multiplier":10}}}`)
	if status != http.StatusOK {
		t.Fatalf("good PUT: status %d, want 200 (body %s)", status, body)
	}
	// Applied live.
	if got := s.cfg.Get().Target.Ratio.Multiplier; got != 10 {
		t.Errorf("live multiplier = %v, want 10", got)
	}
	// Persisted to disk.
	persisted, err := config.Load(s.cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Target.Ratio.Multiplier != 10 {
		t.Errorf("persisted multiplier = %v, want 10", persisted.Target.Ratio.Multiplier)
	}
	// Audited with a diff that names the changed field.
	records, err := s.metering.Audit(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("audit records = %d, want 1", len(records))
	}
	if !strings.Contains(records[0].Change, "multiplier") {
		t.Errorf("audit change %q does not mention multiplier", records[0].Change)
	}
}

func TestControlStartStopPersistsAndAudits(t *testing.T) {
	s, ts := newTestServer(t, "")

	if status, _ := req(t, "POST", ts.URL+"/api/control/stop", "", ""); status != http.StatusOK {
		t.Fatalf("stop: status %d", status)
	}
	if s.cfg.Get().General.Running {
		t.Error("running still true after stop")
	}
	persisted, err := config.Load(s.cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.General.Running {
		t.Error("stop was not persisted to config.json")
	}
	if status, _ := req(t, "POST", ts.URL+"/api/control/start", "", ""); status != http.StatusOK {
		t.Fatalf("start: status %d", status)
	}
	if !s.cfg.Get().General.Running {
		t.Error("running still false after start")
	}
	records, _ := s.metering.Audit(0)
	if len(records) != 2 {
		t.Errorf("audit records = %d, want 2 (stop + start)", len(records))
	}
}

func TestResetCounters(t *testing.T) {
	s, ts := newTestServer(t, "")
	s.state.TotalUpload = 12345
	s.state.TotalDownload = 6789

	if status, _ := req(t, "POST", ts.URL+"/api/control/reset-counters", "", ""); status != http.StatusOK {
		t.Fatalf("reset: status %d", status)
	}
	if s.state.TotalUpload != 0 || s.state.TotalDownload != 0 {
		t.Errorf("after reset: up=%d down=%d, want 0/0",
			s.state.TotalUpload, s.state.TotalDownload)
	}
	// tracked = raw + sync must stay 0: sync anchored to -raw.
	if s.state.UploadSync != -5000 || s.state.DownloadSync != -9000 {
		t.Errorf("sync not anchored to -raw: up=%d down=%d",
			s.state.UploadSync, s.state.DownloadSync)
	}
}

func TestNotFound(t *testing.T) {
	_, ts := newTestServer(t, "")
	if status, _ := req(t, "GET", ts.URL+"/nope", "", ""); status != http.StatusNotFound {
		t.Errorf("unknown path: status %d, want 404", status)
	}
}
