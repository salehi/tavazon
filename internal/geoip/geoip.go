// Package geoip wraps the MaxMind reader and builds an ASN-to-prefixes index.
// See docs/project.md §6.5, §7.4.
package geoip

import (
	"fmt"
	"net"
	"sort"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

// ASNInfo describes one autonomous system discovered in the databases.
type ASNInfo struct {
	Number   uint32
	Name     string
	Country  string
	Prefixes []net.IPNet
	NumIPs   uint64
}

// GeoIP is an immutable in-memory index of autonomous systems built from
// MaxMind GeoLite2 databases. It is safe for concurrent reads.
type GeoIP struct {
	asnReader *maxminddb.Reader // nil when built via NewForTest
	byASN     map[uint32]*ASNInfo
}

type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

type countryRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

// Open loads GeoLite2-ASN and GeoLite2-Country databases from disk and builds
// the ASN index. Only IPv4 networks are indexed (Tavazon is IPv4-only).
func Open(asnPath, countryPath string) (*GeoIP, error) {
	asnReader, err := maxminddb.Open(asnPath)
	if err != nil {
		return nil, fmt.Errorf("geoip: open ASN database %q: %w", asnPath, err)
	}
	countryReader, err := maxminddb.Open(countryPath)
	if err != nil {
		asnReader.Close()
		return nil, fmt.Errorf("geoip: open Country database %q: %w", countryPath, err)
	}
	defer countryReader.Close()

	prefixes := make(map[uint32][]net.IPNet)
	names := make(map[uint32]string)

	walker := asnReader.Networks(maxminddb.SkipAliasedNetworks)
	for walker.Next() {
		var rec asnRecord
		subnet, err := walker.Network(&rec)
		if err != nil {
			asnReader.Close()
			return nil, fmt.Errorf("geoip: walk ASN database: %w", err)
		}
		if rec.Number == 0 || len(subnet.IP) != net.IPv4len {
			continue // no ASN, or an IPv6 network
		}
		prefixes[rec.Number] = append(prefixes[rec.Number], copyNet(subnet))
		if names[rec.Number] == "" {
			names[rec.Number] = rec.Org
		}
	}
	if err := walker.Err(); err != nil {
		asnReader.Close()
		return nil, fmt.Errorf("geoip: walk ASN database: %w", err)
	}

	countries := make(map[uint32]string, len(prefixes))
	for asn, nets := range prefixes {
		countries[asn] = majorityCountry(countryReader, nets)
	}

	return &GeoIP{
		asnReader: asnReader,
		byASN:     buildIndex(prefixes, names, countries),
	}, nil
}

// NewForTest builds a GeoIP index from in-memory maps with no file I/O. It is
// the test seam used by the targets, engine, and end-to-end tests.
func NewForTest(prefixes map[uint32][]net.IPNet, country map[uint32]string) *GeoIP {
	names := make(map[uint32]string, len(prefixes))
	for asn := range prefixes {
		names[asn] = fmt.Sprintf("AS%d", asn)
	}
	return &GeoIP{byASN: buildIndex(prefixes, names, country)}
}

func copyNet(n *net.IPNet) net.IPNet {
	return net.IPNet{
		IP:   append(net.IP(nil), n.IP...),
		Mask: append(net.IPMask(nil), n.Mask...),
	}
}

func majorityCountry(r *maxminddb.Reader, nets []net.IPNet) string {
	counts := make(map[string]int)
	for i := range nets {
		var rec countryRecord
		if err := r.Lookup(nets[i].IP, &rec); err != nil {
			continue
		}
		if rec.Country.ISOCode != "" {
			counts[rec.Country.ISOCode]++
		}
	}
	best, bestN := "", 0
	for c, n := range counts {
		if n > bestN {
			best, bestN = c, n
		}
	}
	return best
}

func buildIndex(prefixes map[uint32][]net.IPNet, names, countries map[uint32]string) map[uint32]*ASNInfo {
	byASN := make(map[uint32]*ASNInfo, len(prefixes))
	for asn, nets := range prefixes {
		info := &ASNInfo{
			Number:   asn,
			Name:     names[asn],
			Country:  countries[asn],
			Prefixes: nets,
		}
		for i := range nets {
			ones, bits := nets[i].Mask.Size()
			if bits == 32 {
				info.NumIPs += uint64(1) << uint(32-ones)
			}
		}
		byASN[asn] = info
	}
	return byASN
}

// ListASNs returns the indexed autonomous systems, sorted by AS number. An
// empty country returns every ASN; otherwise only ASNs in that ISO country.
func (g *GeoIP) ListASNs(country string) []ASNInfo {
	out := make([]ASNInfo, 0, len(g.byASN))
	for _, info := range g.byASN {
		if country != "" && info.Country != country {
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// Prefixes returns the IPv4 networks of the given ASN, or nil if unknown.
func (g *GeoIP) Prefixes(asn uint32) []net.IPNet {
	if info, ok := g.byASN[asn]; ok {
		return info.Prefixes
	}
	return nil
}

// LookupASN returns the AS number an IP belongs to, if known.
func (g *GeoIP) LookupASN(ip net.IP) (uint32, bool) {
	if g.asnReader != nil {
		var rec asnRecord
		if err := g.asnReader.Lookup(ip, &rec); err == nil && rec.Number != 0 {
			return rec.Number, true
		}
		return 0, false
	}
	for asn, info := range g.byASN {
		for i := range info.Prefixes {
			if info.Prefixes[i].Contains(ip) {
				return asn, true
			}
		}
	}
	return 0, false
}

// Close releases the underlying database reader, if any.
func (g *GeoIP) Close() error {
	if g.asnReader != nil {
		return g.asnReader.Close()
	}
	return nil
}
