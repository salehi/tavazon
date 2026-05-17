package sysstat

import (
	"math"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseStatJiffies(t *testing.T) {
	// A comm with spaces and nested parentheses stresses the last-')' parsing.
	line := "1234 (my proc (x)) S 1 1234 1234 0 -1 4194304 100 0 0 0 50 30 0 0 20 0 5 0\n"
	j, err := parseStatJiffies(line)
	if err != nil {
		t.Fatalf("parseStatJiffies: %v", err)
	}
	if j != 80 {
		t.Errorf("jiffies = %d, want 80 (utime 50 + stime 30)", j)
	}
}

func TestParseKBLine(t *testing.T) {
	status := "Name:\ttavazon\nVmRSS:\t   18432 kB\nVmSize:\t 99999 kB\n"
	rss, err := parseKBLine(strings.NewReader(status), "VmRSS:")
	if err != nil {
		t.Fatalf("parseKBLine VmRSS: %v", err)
	}
	if rss != 18432*1024 {
		t.Errorf("rss = %d, want %d", rss, 18432*1024)
	}

	mem := "MemTotal:       16384000 kB\nMemFree:         8000000 kB\n"
	mt, err := parseKBLine(strings.NewReader(mem), "MemTotal:")
	if err != nil {
		t.Fatalf("parseKBLine MemTotal: %v", err)
	}
	if mt != 16384000*1024 {
		t.Errorf("memtotal = %d, want %d", mt, 16384000*1024)
	}

	if _, err := parseKBLine(strings.NewReader("Name:\tx\n"), "VmRSS:"); err == nil {
		t.Error("expected an error when the prefix is absent")
	}
}

func TestCPUPercent(t *testing.T) {
	t0 := time.Now()
	prev := Sample{CPUJiffies: 1000, At: t0}
	cur := Sample{CPUJiffies: 1200, At: t0.Add(2 * time.Second)}
	// 200 jiffies / 100 Hz = 2 CPU-seconds over 2 wall-seconds.
	got := CPUPercent(prev, cur)
	want := 100.0 / float64(runtime.NumCPU())
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("CPUPercent = %v, want %v", got, want)
	}
}

func TestCPUPercentHandlesReset(t *testing.T) {
	t0 := time.Now()
	prev := Sample{CPUJiffies: 5000, At: t0}
	cur := Sample{CPUJiffies: 10, At: t0.Add(time.Second)}
	if got := CPUPercent(prev, cur); got != 0 {
		t.Errorf("CPUPercent on a backward jiffy count = %v, want 0", got)
	}
}

func TestReadRealProc(t *testing.T) {
	s, err := Read()
	if err != nil {
		t.Skipf("Read failed (not Linux?): %v", err)
	}
	if s.RSSBytes <= 0 {
		t.Errorf("RSSBytes = %d, want positive", s.RSSBytes)
	}
	if s.MemTotalBytes <= 0 {
		t.Errorf("MemTotalBytes = %d, want positive", s.MemTotalBytes)
	}
}
