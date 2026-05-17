// Package sysstat samples the Tavazon process's CPU and memory use from /proc
// for the dashboard resource panel. See docs/project.md §7.11a.
package sysstat

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// clockTicksPerSec is the Linux USER_HZ. It is 100 on every mainstream Linux
// build; hard-coding it avoids a cgo sysconf call.
const clockTicksPerSec = 100

// Sample is one process resource reading.
type Sample struct {
	CPUJiffies    uint64
	RSSBytes      int64
	MemTotalBytes int64
	At            time.Time
}

// Read takes a process resource sample from /proc. It is Linux-only.
func Read() (Sample, error) {
	s := Sample{At: time.Now()}

	statData, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return Sample{}, fmt.Errorf("sysstat: read /proc/self/stat: %w", err)
	}
	s.CPUJiffies, err = parseStatJiffies(string(statData))
	if err != nil {
		return Sample{}, err
	}

	statusF, err := os.Open("/proc/self/status")
	if err != nil {
		return Sample{}, fmt.Errorf("sysstat: open /proc/self/status: %w", err)
	}
	s.RSSBytes, err = parseKBLine(statusF, "VmRSS:")
	statusF.Close()
	if err != nil {
		return Sample{}, err
	}

	memF, err := os.Open("/proc/meminfo")
	if err != nil {
		return Sample{}, fmt.Errorf("sysstat: open /proc/meminfo: %w", err)
	}
	s.MemTotalBytes, err = parseKBLine(memF, "MemTotal:")
	memF.Close()
	if err != nil {
		return Sample{}, err
	}
	return s, nil
}

// parseStatJiffies extracts utime+stime from a /proc/<pid>/stat line. The comm
// field may contain spaces and parentheses, so fields are read after the last
// ')': index 0 is the state, index 11 is utime, index 12 is stime.
func parseStatJiffies(data string) (uint64, error) {
	rparen := strings.LastIndexByte(data, ')')
	if rparen < 0 || rparen+1 >= len(data) {
		return 0, fmt.Errorf("sysstat: malformed /proc/self/stat")
	}
	fields := strings.Fields(data[rparen+1:])
	if len(fields) < 13 {
		return 0, fmt.Errorf("sysstat: /proc/self/stat has too few fields")
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sysstat: utime: %w", err)
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sysstat: stime: %w", err)
	}
	return utime + stime, nil
}

// parseKBLine returns the bytes value of the first "<prefix> <N> kB" line.
func parseKBLine(r io.Reader, prefix string) (int64, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("sysstat: malformed %q line", prefix)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("sysstat: %q value: %w", prefix, err)
		}
		return kb * 1024, nil
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("sysstat: scan: %w", err)
	}
	return 0, fmt.Errorf("sysstat: %q not found", prefix)
}

// CPUPercent derives process CPU usage between two samples, as a percentage of
// one core (so it can exceed 100 only across cores; here it is normalised by
// NumCPU to a 0..100 whole-machine figure). A backward jiffy count yields 0.
func CPUPercent(prev, cur Sample) float64 {
	dt := cur.At.Sub(prev.At).Seconds()
	if dt <= 0 || cur.CPUJiffies < prev.CPUJiffies {
		return 0
	}
	cpuSeconds := float64(cur.CPUJiffies-prev.CPUJiffies) / clockTicksPerSec
	ncpu := float64(runtime.NumCPU())
	if ncpu <= 0 {
		ncpu = 1
	}
	return cpuSeconds / dt / ncpu * 100
}
