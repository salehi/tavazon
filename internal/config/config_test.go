package config

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultValidates(t *testing.T) {
	w, err := Default().Validate()
	if err != nil {
		t.Fatalf("Default() should validate cleanly: %v", err)
	}
	if len(w) != 0 {
		t.Fatalf("Default() should produce no warnings, got %v", w)
	}
}

func TestDefaultRoundTrip(t *testing.T) {
	j1, err := json.Marshal(Default())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(j1, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	j2, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(j1) != string(j2) {
		t.Fatalf("round-trip changed config:\n%s\n%s", j1, j2)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"bad mode", func(c *Config) { c.Target.Mode = "bogus" }},
		{"low multiplier", func(c *Config) { c.Target.Ratio.Multiplier = 1 }},
		{"high multiplier", func(c *Config) { c.Target.Ratio.Multiplier = 99 }},
		{"speed coeff", func(c *Config) { c.Uploader.SpeedCoefficient = 9 }},
		{"threads coeff", func(c *Config) { c.Uploader.ThreadsCoefficient = 0 }},
		{"base rate", func(c *Config) { c.Uploader.BaseRateBPS = 1 }},
		{"cycle fraction", func(c *Config) { c.Uploader.CycleBudgetFraction = 2 }},
		{"datagram order", func(c *Config) { c.Uploader.MaxDatagram = 32 }},
		{"datagram cap", func(c *Config) { c.Uploader.MaxDatagram = 9000 }},
		{"port range", func(c *Config) { c.Targets.PortMin = 0 }},
		{"percentile", func(c *Config) { c.Metering.Percentile = 100 }},
		{"interval order", func(c *Config) {
			c.General.IntervalMin = Duration(time.Minute)
			c.General.IntervalMax = Duration(time.Second)
		}},
		{"log level", func(c *Config) { c.Log.Level = "trace" }},
		{"dev target", func(c *Config) { c.Dev.Enabled = true; c.Dev.Target = "not-an-ip" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			if _, err := cfg.Validate(); err == nil {
				t.Fatalf("expected Validate to reject %s", tt.name)
			}
		})
	}
}

func TestValidateWarnsOnExposedDashboard(t *testing.T) {
	cfg := Default()
	cfg.Web.Listen = "0.0.0.0:8081"
	cfg.Web.AuthToken = ""
	w, err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(w) == 0 {
		t.Fatalf("expected a warning for a non-local listen with no auth token")
	}
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("TAVAZON_TARGET_RATIO_MULTIPLIER", "10")
	t.Setenv("TAVAZON_WEB_LISTEN", "0.0.0.0:9000")
	t.Setenv("TAVAZON_GENERAL_RUNNING", "false")
	t.Setenv("TAVAZON_STATE_SAVE_INTERVAL", "45s")
	cfg := Default()
	if err := cfg.ApplyEnv(); err != nil {
		t.Fatalf("ApplyEnv: %v", err)
	}
	if cfg.Target.Ratio.Multiplier != 10 {
		t.Errorf("multiplier = %v, want 10", cfg.Target.Ratio.Multiplier)
	}
	if cfg.Web.Listen != "0.0.0.0:9000" {
		t.Errorf("listen = %q, want 0.0.0.0:9000", cfg.Web.Listen)
	}
	if cfg.General.Running {
		t.Errorf("running = true, want false")
	}
	if cfg.State.SaveInterval != Duration(45*time.Second) {
		t.Errorf("save_interval = %v, want 45s", cfg.State.SaveInterval.Std())
	}
}

func TestPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"target":{"ratio":{"multiplier":5}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Target.Ratio.Multiplier != 5 {
		t.Fatalf("file overlay: multiplier = %v, want 5", cfg.Target.Ratio.Multiplier)
	}
	if cfg.Target.Mode != "ratio" {
		t.Fatalf("file overlay clobbered an absent field: mode = %q", cfg.Target.Mode)
	}

	t.Setenv("TAVAZON_TARGET_RATIO_MULTIPLIER", "10")
	if err := cfg.ApplyEnv(); err != nil {
		t.Fatalf("ApplyEnv: %v", err)
	}
	if cfg.Target.Ratio.Multiplier != 10 {
		t.Fatalf("env beats file: multiplier = %v, want 10", cfg.Target.Ratio.Multiplier)
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Int("multiplier", 0, "")
	if err := fs.Parse([]string{"-multiplier", "12"}); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyFlags(fs)
	if cfg.Target.Ratio.Multiplier != 12 {
		t.Fatalf("flag beats env: multiplier = %v, want 12", cfg.Target.Ratio.Multiplier)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load of a missing file should not error: %v", err)
	}
	if cfg.Target.Ratio.Multiplier != 8 {
		t.Fatalf("missing file should yield defaults, got multiplier %v", cfg.Target.Ratio.Multiplier)
	}
}
