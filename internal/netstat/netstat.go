// Package netstat reads per-interface RX/TX byte counters from /proc/net/dev.
// See docs/project.md §6.1, §7.3.
package netstat

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const procNetDev = "/proc/net/dev"

// Counters holds interface byte counters. TxBytes is egress ("upload"),
// RxBytes is ingress ("download").
type Counters struct {
	TxBytes uint64
	RxBytes uint64
}

// Read returns counters summed over the selected interfaces.
//   - iface == "" : every interface except loopback ("lo")
//   - otherwise   : only the named interface
func Read(iface string) (Counters, error) {
	f, err := os.Open(procNetDev)
	if err != nil {
		return Counters{}, fmt.Errorf("netstat: open %s: %w", procNetDev, err)
	}
	defer f.Close()
	return parse(f, iface)
}

// parse reads /proc/net/dev-formatted data from r. It is split out so tests
// can supply a fixture instead of the real file.
func parse(r io.Reader, iface string) (Counters, error) {
	var total Counters
	found := false
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		if line <= 2 {
			continue // two header lines
		}
		text := sc.Text()
		colon := strings.IndexByte(text, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(text[:colon])
		fields := strings.Fields(text[colon+1:])
		if len(fields) < 16 {
			return Counters{}, fmt.Errorf("netstat: malformed line for %q", name)
		}
		if iface == "" {
			if name == "lo" {
				continue
			}
		} else if name != iface {
			continue
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return Counters{}, fmt.Errorf("netstat: rx bytes for %q: %w", name, err)
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return Counters{}, fmt.Errorf("netstat: tx bytes for %q: %w", name, err)
		}
		total.RxBytes += rx
		total.TxBytes += tx
		found = true
	}
	if err := sc.Err(); err != nil {
		return Counters{}, fmt.Errorf("netstat: read: %w", err)
	}
	if iface != "" && !found {
		return Counters{}, fmt.Errorf("netstat: interface %q not found", iface)
	}
	return total, nil
}

// Interfaces returns the names of every interface in /proc/net/dev except
// loopback. It feeds the dashboard's interface picker.
func Interfaces() ([]string, error) {
	f, err := os.Open(procNetDev)
	if err != nil {
		return nil, fmt.Errorf("netstat: open %s: %w", procNetDev, err)
	}
	defer f.Close()
	var names []string
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		if line <= 2 {
			continue
		}
		text := sc.Text()
		colon := strings.IndexByte(text, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(text[:colon])
		if name == "" || name == "lo" {
			continue
		}
		names = append(names, name)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("netstat: read: %w", err)
	}
	return names, nil
}
