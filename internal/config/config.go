// Package config loads, validates, and overlays Tavazon configuration from a
// JSON file, environment variables, and CLI flags. See docs/project.md §7.2, §8.
package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that marshals to and from a Go duration string
// ("5s", "30m") in JSON, rather than an integer nanosecond count.
type Duration time.Duration

// Std returns the value as a standard time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalJSON renders the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON parses a Go duration string into the duration.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Config is the full Tavazon configuration. See docs/project.md §8 for the
// JSON schema and the documented defaults.
type Config struct {
	General  GeneralConfig  `json:"general"`
	Dev      DevConfig      `json:"dev"`
	Target   TargetConfig   `json:"target"`
	Network  NetworkConfig  `json:"network"`
	GeoIP    GeoIPConfig    `json:"geoip"`
	Uploader UploaderConfig `json:"uploader"`
	Curve    CurveConfig    `json:"curve"`
	Targets  TargetsConfig  `json:"targets"`
	State    StateConfig    `json:"state"`
	Metering MeteringConfig `json:"metering"`
	Web      WebConfig      `json:"web"`
	Log      LogConfig      `json:"log"`
}

// GeneralConfig holds engine-cycle timing and the persisted run state.
type GeneralConfig struct {
	IntervalMin Duration `json:"interval_min"`
	IntervalMax Duration `json:"interval_max"`
	Running     bool     `json:"running"`
}

// DevConfig holds the local-testing mode settings (docs/project.md §6.6).
type DevConfig struct {
	Enabled bool   `json:"enabled"`
	Target  string `json:"target"`
	Workers int    `json:"workers"`
}

// TargetConfig selects the target mode and its parameters.
type TargetConfig struct {
	Mode   string       `json:"mode"`
	Ratio  RatioConfig  `json:"ratio"`
	Volume VolumeConfig `json:"volume"`
}

// RatioConfig parameterises ratio mode: upload = download x multiplier.
type RatioConfig struct {
	Multiplier      float64 `json:"multiplier"`
	Jitter          float64 `json:"jitter"`
	MinDeficitBytes int64   `json:"min_deficit_bytes"`
}

// VolumeConfig parameterises volume mode: Bytes pushed over Window.
type VolumeConfig struct {
	Bytes  int64    `json:"bytes"`
	Window Duration `json:"window"`
}

// NetworkConfig selects the measured interface and its link speed.
type NetworkConfig struct {
	Interface        string `json:"interface"`
	LinkCapacityMbit int    `json:"link_capacity_mbit"`
}

// GeoIPConfig holds the MaxMind database paths and the selected ASNs.
type GeoIPConfig struct {
	ASNDB         string   `json:"asn_db"`
	CountryDB     string   `json:"country_db"`
	PickerCountry string   `json:"picker_country"`
	SelectedASNs  []uint32 `json:"selected_asns"`
}

// UploaderConfig holds the worker-pool and rate-limiter tunables.
type UploaderConfig struct {
	ThreadsCoefficient  int      `json:"threads_coefficient"`
	SpeedCoefficient    int      `json:"speed_coefficient"`
	BaseRateBPS         int64    `json:"base_rate_bps"`
	CycleBudgetFraction float64  `json:"cycle_budget_fraction"`
	PacketGapMax        Duration `json:"packet_gap_max"`
	MaxWorkers          int      `json:"max_workers"`
	MaxRamp             int      `json:"max_ramp"`
	MinDatagram         int      `json:"min_datagram"`
	MaxDatagram         int      `json:"max_datagram"`
}

// CurveConfig holds the continuous 24-hour traffic curve (docs/project.md §6.4).
type CurveConfig struct {
	Anchors         [24]float64 `json:"anchors"`
	Max             float64     `json:"max"`
	WanderStrength  float64     `json:"wander_strength"`
	WanderReversion Duration    `json:"wander_reversion"`
}

// TargetsConfig holds the destination port range and IP-cache TTL bounds.
type TargetsConfig struct {
	PortMin     int      `json:"port_min"`
	PortMax     int      `json:"port_max"`
	CacheTTLMin Duration `json:"cache_ttl_min"`
	CacheTTLMax Duration `json:"cache_ttl_max"`
}

// StateConfig holds the persistent-state file path and save cadence.
type StateConfig struct {
	File         string   `json:"file"`
	SaveInterval Duration `json:"save_interval"`
}

// MeteringConfig holds the time-series store settings.
type MeteringConfig struct {
	Dir           string   `json:"dir"`
	Retention5Min Duration `json:"retention_5min"`
	BillingWindow string   `json:"billing_window"`
	Percentile    int      `json:"percentile"`
}

// WebConfig holds the dashboard HTTP server settings.
type WebConfig struct {
	Enabled   bool   `json:"enabled"`
	Listen    string `json:"listen"`
	AuthToken string `json:"auth_token"`
}

// LogConfig holds the logger settings.
type LogConfig struct {
	File       string `json:"file"`
	Level      string `json:"level"`
	MaxSizeMB  int    `json:"max_size_mb"`
	MaxBackups int    `json:"max_backups"`
}

// Default returns a Config populated with the docs/project.md §8 defaults.
func Default() *Config {
	return &Config{
		General: GeneralConfig{
			IntervalMin: Duration(5 * time.Second),
			IntervalMax: Duration(30 * time.Second),
			Running:     true,
		},
		Dev: DevConfig{
			Enabled: false,
			Target:  "192.168.1.1",
			Workers: 4,
		},
		Target: TargetConfig{
			Mode: "ratio",
			Ratio: RatioConfig{
				Multiplier:      8,
				Jitter:          0.3,
				MinDeficitBytes: 1073741824,
			},
			Volume: VolumeConfig{
				Bytes:  1073741824,
				Window: Duration(24 * time.Hour),
			},
		},
		Network: NetworkConfig{
			Interface:        "",
			LinkCapacityMbit: 0,
		},
		GeoIP: GeoIPConfig{
			ASNDB:         "maxmind_files/GeoLite2-ASN.mmdb",
			CountryDB:     "maxmind_files/GeoLite2-Country.mmdb",
			PickerCountry: "IR",
			SelectedASNs:  []uint32{},
		},
		Uploader: UploaderConfig{
			ThreadsCoefficient:  3,
			SpeedCoefficient:    1,
			BaseRateBPS:         524288,
			CycleBudgetFraction: 0.3,
			PacketGapMax:        Duration(10 * time.Millisecond),
			MaxWorkers:          300,
			MaxRamp:             20,
			MinDatagram:         64,
			MaxDatagram:         1472,
		},
		Curve: CurveConfig{
			Anchors: [24]float64{
				0.6, 0.4, 0.3, 0.2, 0.05, 0.05, 0.2, 0.6, 1.0, 1.2, 1.3, 1.4,
				1.3, 1.35, 1.5, 1.45, 1.3, 1.45, 1.7, 1.9, 2.0, 1.5, 1.1, 0.8,
			},
			Max:             2.5,
			WanderStrength:  0.15,
			WanderReversion: Duration(30 * time.Minute),
		},
		Targets: TargetsConfig{
			PortMin:     1024,
			PortMax:     65535,
			CacheTTLMin: Duration(10 * time.Minute),
			CacheTTLMax: Duration(100 * time.Minute),
		},
		State: StateConfig{
			File:         "data/state.json",
			SaveInterval: Duration(30 * time.Second),
		},
		Metering: MeteringConfig{
			Dir:           "data/metering",
			Retention5Min: Duration(9000 * time.Hour),
			BillingWindow: "month",
			Percentile:    95,
		},
		Web: WebConfig{
			Enabled:   true,
			Listen:    "127.0.0.1:8080",
			AuthToken: "",
		},
		Log: LogConfig{
			File:       "data/tavazon.log",
			Level:      "info",
			MaxSizeMB:  10,
			MaxBackups: 3,
		},
	}
}

// Load reads config from path, overlaying it on Default. A missing file is not
// an error: the defaults are returned unchanged.
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	return cfg, nil
}

var durationType = reflect.TypeOf(Duration(0))

// ApplyEnv overlays TAVAZON_<SECTION>_<FIELD> environment variables onto the
// config. Only scalar fields are reachable; slices and arrays are skipped.
func (c *Config) ApplyEnv() error {
	return applyEnv(reflect.ValueOf(c).Elem(), "TAVAZON")
}

func applyEnv(v reflect.Value, prefix string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		key := prefix + "_" + strings.ToUpper(name)
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct && fv.Type() != durationType {
			if err := applyEnv(fv, key); err != nil {
				return err
			}
			continue
		}
		if fv.Kind() == reflect.Slice || fv.Kind() == reflect.Array {
			continue
		}
		env, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		if err := setScalar(fv, env); err != nil {
			return fmt.Errorf("env %s: %w", key, err)
		}
	}
	return nil
}

func setScalar(fv reflect.Value, s string) error {
	if fv.Type() == durationType {
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}

// ApplyFlags overlays the values of CLI flags that were explicitly set onto
// the config. Flag definitions live in cmd/tavazon (docs/project.md §12).
func (c *Config) ApplyFlags(fs *flag.FlagSet) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "state":
			c.State.File = f.Value.String()
		case "asn-db":
			c.GeoIP.ASNDB = f.Value.String()
		case "country-db":
			c.GeoIP.CountryDB = f.Value.String()
		case "listen":
			c.Web.Listen = f.Value.String()
		case "mode":
			c.Target.Mode = f.Value.String()
		case "multiplier":
			if n, err := strconv.Atoi(f.Value.String()); err == nil {
				c.Target.Ratio.Multiplier = float64(n)
			}
		case "stopped":
			c.General.Running = false
		case "no-web":
			c.Web.Enabled = false
		case "log-level":
			c.Log.Level = f.Value.String()
		}
	})
}

// Validate range-checks the configuration. It returns a list of non-fatal
// warnings and an error for the first hard failure found. It does no I/O: the
// existence of the .mmdb files is checked when geoip opens them.
func (c *Config) Validate() (warnings []string, err error) {
	if c.General.IntervalMin <= 0 || c.General.IntervalMax <= 0 {
		return nil, fmt.Errorf("general: interval_min and interval_max must be positive")
	}
	if c.General.IntervalMin > c.General.IntervalMax {
		return nil, fmt.Errorf("general: interval_min must not exceed interval_max")
	}
	switch c.Target.Mode {
	case "ratio", "volume":
	default:
		return nil, fmt.Errorf("target.mode %q: must be \"ratio\" or \"volume\"", c.Target.Mode)
	}
	if c.Target.Ratio.Multiplier < 2 || c.Target.Ratio.Multiplier > 15 {
		return nil, fmt.Errorf("target.ratio.multiplier %g: must be in [2,15]", c.Target.Ratio.Multiplier)
	}
	if c.Target.Ratio.Jitter < 0 || c.Target.Ratio.Jitter > 0.9 {
		return nil, fmt.Errorf("target.ratio.jitter %g: must be in [0,0.9]", c.Target.Ratio.Jitter)
	}
	if c.Target.Ratio.MinDeficitBytes < 0 {
		return nil, fmt.Errorf("target.ratio.min_deficit_bytes must not be negative")
	}
	if c.Target.Volume.Bytes <= 0 {
		return nil, fmt.Errorf("target.volume.bytes must be positive")
	}
	if c.Target.Volume.Window <= 0 {
		return nil, fmt.Errorf("target.volume.window must be positive")
	}
	if c.Uploader.ThreadsCoefficient < 1 || c.Uploader.ThreadsCoefficient > 30 {
		return nil, fmt.Errorf("uploader.threads_coefficient %d: must be in [1,30]", c.Uploader.ThreadsCoefficient)
	}
	if c.Uploader.SpeedCoefficient < 1 || c.Uploader.SpeedCoefficient > 5 {
		return nil, fmt.Errorf("uploader.speed_coefficient %d: must be in [1,5]", c.Uploader.SpeedCoefficient)
	}
	if c.Uploader.BaseRateBPS < 65536 {
		return nil, fmt.Errorf("uploader.base_rate_bps %d: minimum is 65536", c.Uploader.BaseRateBPS)
	}
	if c.Uploader.CycleBudgetFraction < 0.05 || c.Uploader.CycleBudgetFraction > 1.0 {
		return nil, fmt.Errorf("uploader.cycle_budget_fraction %g: must be in [0.05,1.0]", c.Uploader.CycleBudgetFraction)
	}
	if c.Uploader.PacketGapMax < 0 {
		return nil, fmt.Errorf("uploader.packet_gap_max must not be negative")
	}
	if c.Uploader.MaxWorkers < 1 {
		return nil, fmt.Errorf("uploader.max_workers must be at least 1")
	}
	if c.Uploader.MaxRamp < 1 {
		return nil, fmt.Errorf("uploader.max_ramp must be at least 1")
	}
	if c.Uploader.MinDatagram < 1 {
		return nil, fmt.Errorf("uploader.min_datagram must be at least 1")
	}
	if c.Uploader.MaxDatagram <= c.Uploader.MinDatagram || c.Uploader.MaxDatagram > 1472 {
		return nil, fmt.Errorf("uploader.max_datagram %d: must be in (min_datagram,1472]", c.Uploader.MaxDatagram)
	}
	if c.Curve.Max <= 0 {
		return nil, fmt.Errorf("curve.max must be positive")
	}
	if c.Curve.WanderStrength < 0 {
		return nil, fmt.Errorf("curve.wander_strength must not be negative")
	}
	if c.Curve.WanderReversion <= 0 {
		return nil, fmt.Errorf("curve.wander_reversion must be positive")
	}
	if c.Targets.PortMin < 1 || c.Targets.PortMax > 65535 || c.Targets.PortMin >= c.Targets.PortMax {
		return nil, fmt.Errorf("targets: require 1 <= port_min < port_max <= 65535")
	}
	if c.Targets.CacheTTLMin <= 0 || c.Targets.CacheTTLMax <= 0 || c.Targets.CacheTTLMin > c.Targets.CacheTTLMax {
		return nil, fmt.Errorf("targets: require 0 < cache_ttl_min <= cache_ttl_max")
	}
	if c.State.SaveInterval <= 0 {
		return nil, fmt.Errorf("state.save_interval must be positive")
	}
	if c.Metering.Retention5Min <= 0 {
		return nil, fmt.Errorf("metering.retention_5min must be positive")
	}
	if c.Metering.Percentile < 50 || c.Metering.Percentile > 99 {
		return nil, fmt.Errorf("metering.percentile %d: must be in [50,99]", c.Metering.Percentile)
	}
	if c.Network.LinkCapacityMbit < 0 {
		return nil, fmt.Errorf("network.link_capacity_mbit must not be negative")
	}
	if c.Log.MaxSizeMB < 1 || c.Log.MaxBackups < 0 {
		return nil, fmt.Errorf("log: require max_size_mb >= 1 and max_backups >= 0")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return nil, fmt.Errorf("log.level %q: must be debug|info|warn|error", c.Log.Level)
	}
	if c.Dev.Enabled {
		if net.ParseIP(c.Dev.Target) == nil {
			return nil, fmt.Errorf("dev.target %q: not a valid IP address", c.Dev.Target)
		}
		if c.Dev.Workers < 1 {
			return nil, fmt.Errorf("dev.workers must be at least 1")
		}
	}
	if !isLocalListen(c.Web.Listen) && c.Web.AuthToken == "" {
		warnings = append(warnings, fmt.Sprintf(
			"web.listen %q is non-local but auth_token is empty — the dashboard is unprotected",
			c.Web.Listen))
	}
	return warnings, nil
}

// isLocalListen reports whether addr binds only the loopback interface.
func isLocalListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}
