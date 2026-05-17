package targets

import (
	"math/rand/v2"
	"net"
	"testing"

	"namizungo/internal/config"
	"namizungo/internal/geoip"
)

func mustCIDR(t *testing.T, s string) net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad CIDR %q: %v", s, err)
	}
	return *n
}

func testGeoIP(t *testing.T) *geoip.GeoIP {
	return geoip.NewForTest(
		map[uint32][]net.IPNet{
			100: {mustCIDR(t, "5.1.0.0/16")},
			200: {mustCIDR(t, "2.144.0.0/16"), mustCIDR(t, "37.32.0.0/19")},
			300: {mustCIDR(t, "8.8.8.0/24")},
		},
		map[uint32]string{100: "IR", 200: "IR", 300: "US"},
	)
}

func testCfg() config.TargetsConfig {
	return config.TargetsConfig{PortMin: 1024, PortMax: 65535}
}

func TestNewEmptySelection(t *testing.T) {
	tg, skipped, err := New(testGeoIP(t), nil, testCfg(), rand.New(rand.NewPCG(1, 2)))
	if err != nil {
		t.Fatalf("New with no ASNs should not error: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want empty", skipped)
	}
	if !tg.Empty() {
		t.Error("Targets with no ASNs should be Empty")
	}
}

func TestNewSkipsUnknownASN(t *testing.T) {
	tg, skipped, err := New(testGeoIP(t), []uint32{100, 999}, testCfg(), rand.New(rand.NewPCG(1, 2)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != 999 {
		t.Errorf("skipped = %v, want [999]", skipped)
	}
	if tg.Empty() {
		t.Error("Targets should not be Empty: ASN 100 resolved")
	}
}

func TestNewAllUnknownErrors(t *testing.T) {
	if _, _, err := New(testGeoIP(t), []uint32{999, 998}, testCfg(), rand.New(rand.NewPCG(1, 2))); err == nil {
		t.Fatal("New should error when no selected ASN resolves")
	}
}

func TestRandomTargetCoverageAndBounds(t *testing.T) {
	g := testGeoIP(t)
	prefixes := map[uint32][]net.IPNet{
		100: g.Prefixes(100),
		200: g.Prefixes(200),
		300: g.Prefixes(300),
	}
	cfg := testCfg()
	tg, _, err := New(g, []uint32{100, 200, 300}, cfg, rand.New(rand.NewPCG(42, 7)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seen := map[uint32]bool{}
	for i := 0; i < 10000; i++ {
		ip, port, asn := tg.RandomTarget()
		seen[asn] = true
		if port < cfg.PortMin || port > cfg.PortMax {
			t.Fatalf("port %d outside [%d,%d]", port, cfg.PortMin, cfg.PortMax)
		}
		inside := false
		for _, p := range prefixes[asn] {
			if p.Contains(ip) {
				inside = true
				break
			}
		}
		if !inside {
			t.Fatalf("IP %v is not inside any prefix of ASN %d", ip, asn)
		}
	}
	for _, asn := range []uint32{100, 200, 300} {
		if !seen[asn] {
			t.Errorf("ASN %d was never selected over 10000 draws", asn)
		}
	}
}
