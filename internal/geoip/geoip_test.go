package geoip

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func mustCIDR(t *testing.T, s string) net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad CIDR %q: %v", s, err)
	}
	return *n
}

func TestNewForTest(t *testing.T) {
	prefixes := map[uint32][]net.IPNet{
		100: {mustCIDR(t, "5.1.0.0/16")},
		200: {mustCIDR(t, "2.144.0.0/16"), mustCIDR(t, "37.32.0.0/19")},
		300: {mustCIDR(t, "8.8.8.0/24")},
	}
	country := map[uint32]string{100: "IR", 200: "IR", 300: "US"}
	g := NewForTest(prefixes, country)

	if asn, ok := g.LookupASN(net.ParseIP("5.1.2.3")); !ok || asn != 100 {
		t.Errorf("LookupASN(5.1.2.3) = %d,%v want 100,true", asn, ok)
	}
	if asn, ok := g.LookupASN(net.ParseIP("8.8.8.8")); !ok || asn != 300 {
		t.Errorf("LookupASN(8.8.8.8) = %d,%v want 300,true", asn, ok)
	}
	if _, ok := g.LookupASN(net.ParseIP("1.1.1.1")); ok {
		t.Error("LookupASN(1.1.1.1) should miss")
	}
	if got := g.Prefixes(200); len(got) != 2 {
		t.Errorf("Prefixes(200) len = %d, want 2", len(got))
	}
	if got := g.Prefixes(999); got != nil {
		t.Errorf("Prefixes(999) = %v, want nil", got)
	}

	ir := g.ListASNs("IR")
	if len(ir) != 2 {
		t.Errorf("ListASNs(IR) len = %d, want 2", len(ir))
	}
	for _, a := range ir {
		if a.Country != "IR" {
			t.Errorf("ListASNs(IR) returned ASN %d with country %q", a.Number, a.Country)
		}
	}
	if got := g.ListASNs(""); len(got) != 3 {
		t.Errorf("ListASNs(\"\") len = %d, want 3", len(got))
	}
	for _, a := range g.ListASNs("US") {
		if a.Number == 300 && a.NumIPs != 256 {
			t.Errorf("ASN 300 (a /24) NumIPs = %d, want 256", a.NumIPs)
		}
	}
}

func realDBPaths(t *testing.T) (asn, country string) {
	t.Helper()
	asn = os.Getenv("TAVAZON_TEST_ASN_DB")
	if asn == "" {
		asn = filepath.Join("..", "..", "maxmind_files", "GeoLite2-ASN.mmdb")
	}
	country = os.Getenv("TAVAZON_TEST_COUNTRY_DB")
	if country == "" {
		country = filepath.Join("..", "..", "maxmind_files", "GeoLite2-Country.mmdb")
	}
	for _, p := range []string{asn, country} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("real GeoLite2 databases not present; skipping (%v)", err)
		}
	}
	return asn, country
}

func TestOpenRealDatabases(t *testing.T) {
	asnPath, countryPath := realDBPaths(t)

	g, err := Open(asnPath, countryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer g.Close()

	ir := g.ListASNs("IR")
	if len(ir) == 0 {
		t.Fatal("ListASNs(IR) returned nothing from the real database")
	}
	for _, a := range ir {
		if a.Country != "IR" {
			t.Errorf("ListASNs(IR) returned ASN %d with country %q", a.Number, a.Country)
		}
		if len(a.Prefixes) == 0 {
			t.Errorf("IR ASN %d has no prefixes", a.Number)
		}
	}
	if all := g.ListASNs(""); len(all) < len(ir) {
		t.Errorf("ListASNs(\"\") (%d) must be a superset of ListASNs(IR) (%d)", len(all), len(ir))
	}

	// An address inside a known IR ASN's prefix must resolve to some ASN.
	probe := ir[0].Prefixes[0].IP
	if _, ok := g.LookupASN(probe); !ok {
		t.Errorf("LookupASN(%v) missed an address from a known prefix", probe)
	}
}
