// Package sysstat samples whole-machine CPU and memory use from /proc for the
// dashboard resource panel. The figures are system-wide (not just this
// process) so the gauges reflect real load on the host. See docs/project.md
// §7.11a.
package sysstat

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Sample is one system resource reading. CPUTotal/CPUIdle are cumulative jiffy
// counters from /proc/stat; CPU use is their delta between two samples.
type Sample struct {
	CPUTotal      uint64 // sum of every field on the aggregate "cpu" line
	CPUIdle       uint64 // idle + iowait jiffies
	MemTotalBytes int64
	MemUsedBytes  int64 // MemTotal - MemAvailable
	At            time.Time
}

// Read takes a system resource sample from /proc. It is Linux-only.
func Read() (Sample, error) {
	s := Sample{At: time.Now()}

	statF, err := os.Open("/proc/stat")
	if err != nil {
		return Sample{}, fmt.Errorf("sysstat: open /proc/stat: %w", err)
	}
	s.CPUTotal, s.CPUIdle, err = parseCPULine(statF)
	statF.Close()
	if err != nil {
		return Sample{}, err
	}

	memF, err := os.Open("/proc/meminfo")
	if err != nil {
		return Sample{}, fmt.Errorf("sysstat: open /proc/meminfo: %w", err)
	}
	total, avail, err := parseMeminfo(memF)
	memF.Close()
	if err != nil {
		return Sample{}, err
	}
	s.MemTotalBytes = total
	if used := total - avail; used >= 0 {
		s.MemUsedBytes = used
	}
	return s, nil
}

// parseCPULine reads the aggregate "cpu" line of /proc/stat and returns the sum
// of all its jiffy fields (total) and idle+iowait (idle). The line looks like:
//
//	cpu  user nice system idle iowait irq softirq steal guest guest_nice
func parseCPULine(r io.Reader) (total, idle uint64, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		for i, f := range fields[1:] {
			v, perr := strconv.ParseUint(f, 10, 64)
			if perr != nil {
				return 0, 0, fmt.Errorf("sysstat: /proc/stat cpu field %d: %w", i, perr)
			}
			total += v
			// fields[1:] index 3 is idle, index 4 is iowait.
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return total, idle, nil
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("sysstat: scan /proc/stat: %w", err)
	}
	return 0, 0, fmt.Errorf("sysstat: no aggregate cpu line in /proc/stat")
}

// parseMeminfo returns MemTotal and MemAvailable in bytes from /proc/meminfo.
func parseMeminfo(r io.Reader) (total, avail int64, err error) {
	sc := bufio.NewScanner(r)
	haveTotal, haveAvail := false, false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			if total, err = kbValue(line); err != nil {
				return 0, 0, err
			}
			haveTotal = true
		case strings.HasPrefix(line, "MemAvailable:"):
			if avail, err = kbValue(line); err != nil {
				return 0, 0, err
			}
			haveAvail = true
		}
		if haveTotal && haveAvail {
			return total, avail, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("sysstat: scan /proc/meminfo: %w", err)
	}
	if !haveTotal {
		return 0, 0, fmt.Errorf("sysstat: MemTotal not found")
	}
	// MemAvailable is absent on very old kernels; treat it as zero used-headroom
	// rather than failing the whole sample.
	return total, avail, nil
}

// kbValue parses the bytes value of a "<label> <N> kB" /proc/meminfo line.
func kbValue(line string) (int64, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, fmt.Errorf("sysstat: malformed meminfo line %q", line)
	}
	kb, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sysstat: meminfo value %q: %w", fields[1], err)
	}
	return kb * 1024, nil
}

// CPUPercent derives whole-machine CPU utilisation between two samples as the
// share of non-idle jiffies, on a 0..100 scale. A backward or empty delta
// (counter reset, identical samples) yields 0.
func CPUPercent(prev, cur Sample) float64 {
	total := int64(cur.CPUTotal) - int64(prev.CPUTotal)
	idle := int64(cur.CPUIdle) - int64(prev.CPUIdle)
	if total <= 0 {
		return 0
	}
	busy := total - idle
	if busy < 0 {
		busy = 0
	}
	return float64(busy) / float64(total) * 100
}
