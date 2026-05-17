// Package targets turns the operator's selected AS numbers into concrete
// random destination IPs and ports. See docs/project.md §6.6, §7.7.
package targets

import (
	"fmt"
	"math/rand/v2"
	"net"
	"sync"

	"github.com/salehi/tavazon/internal/config"
	"github.com/salehi/tavazon/internal/geoip"
)

type asnPool struct {
	asn      uint32
	prefixes []net.IPNet
	weight   uint64 // total addressable IPv4 count
}

// Targets generates random destination IP/port pairs spread across the
// operator's selected ASNs. RandomTarget is safe for concurrent use.
type Targets struct {
	mu          sync.Mutex
	pools       []asnPool
	totalWeight uint64
	cfg         config.TargetsConfig
	rng         *rand.Rand
}

// New resolves the selected ASNs to their IPv4 prefixes via g. ASNs the
// database does not know are returned in skipped and otherwise ignored. New
// errors only if ASNs were requested but none resolved; an empty selectedASNs
// list yields an idle (Empty) Targets with no error.
func New(g *geoip.GeoIP, selectedASNs []uint32, cfg config.TargetsConfig, rng *rand.Rand) (t *Targets, skipped []uint32, err error) {
	t = &Targets{cfg: cfg, rng: rng}
	for _, asn := range selectedASNs {
		prefixes := g.Prefixes(asn)
		var w uint64
		for i := range prefixes {
			w += prefixSize(prefixes[i])
		}
		if w == 0 {
			skipped = append(skipped, asn)
			continue
		}
		t.pools = append(t.pools, asnPool{asn: asn, prefixes: prefixes, weight: w})
		t.totalWeight += w
	}
	if len(selectedASNs) > 0 && len(t.pools) == 0 {
		return nil, skipped, fmt.Errorf("targets: none of the %d selected ASNs resolved to an IPv4 prefix", len(selectedASNs))
	}
	return t, skipped, nil
}

// Empty reports whether no ASN resolved, i.e. the uploader should stay idle.
func (t *Targets) Empty() bool { return len(t.pools) == 0 }

// RandomTarget picks a random destination: an ASN weighted by its IP count, a
// prefix within it weighted by prefix size, a uniform host inside that prefix,
// and a uniform port in [PortMin, PortMax]. The chosen ASN is returned so
// metering can attribute the bytes.
func (t *Targets) RandomTarget() (ip net.IP, port int, asn uint32) {
	if len(t.pools) == 0 {
		return nil, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	pool := t.pickPool()
	prefix := pickPrefix(pool, t.rng)
	ip = randomHost(prefix, t.rng)
	port = t.cfg.PortMin + t.rng.IntN(t.cfg.PortMax-t.cfg.PortMin+1)
	return ip, port, pool.asn
}

func (t *Targets) pickPool() asnPool {
	r := t.rng.Uint64N(t.totalWeight)
	for _, p := range t.pools {
		if r < p.weight {
			return p
		}
		r -= p.weight
	}
	return t.pools[len(t.pools)-1]
}

func pickPrefix(p asnPool, rng *rand.Rand) net.IPNet {
	r := rng.Uint64N(p.weight)
	for i := range p.prefixes {
		sz := prefixSize(p.prefixes[i])
		if r < sz {
			return p.prefixes[i]
		}
		r -= sz
	}
	return p.prefixes[len(p.prefixes)-1]
}

// prefixSize returns the address count of an IPv4 prefix, or 0 if it is not a
// usable IPv4 network.
func prefixSize(n net.IPNet) uint64 {
	ones, bits := n.Mask.Size()
	if bits != 32 {
		return 0
	}
	return uint64(1) << uint(32-ones)
}

// randomHost returns a uniformly random address inside the IPv4 prefix.
func randomHost(n net.IPNet, rng *rand.Rand) net.IP {
	ip4 := n.IP.To4()
	ones, bits := n.Mask.Size()
	if ip4 == nil || bits != 32 {
		return n.IP
	}
	base := uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
	hostBits := uint(32 - ones)
	var offset uint32
	switch {
	case hostBits >= 32:
		offset = rng.Uint32()
	case hostBits > 0:
		offset = uint32(rng.Uint64N(uint64(1) << hostBits))
	}
	v := base | offset
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
