package sysstat

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseCPULine(t *testing.T) {
	// cpu  user nice system idle iowait irq softirq steal guest guest_nice
	stat := "cpu  100 0 50 800 40 0 10 0 0 0\ncpu0 50 0 25 400 20 0 5 0 0 0\n"
	total, idle, err := parseCPULine(strings.NewReader(stat))
	if err != nil {
		t.Fatalf("parseCPULine: %v", err)
	}
	// total = 100+0+50+800+40+0+10 = 1000; idle = idle(800)+iowait(40) = 840.
	if total != 1000 {
		t.Errorf("total = %d, want 1000", total)
	}
	if idle != 840 {
		t.Errorf("idle = %d, want 840", idle)
	}
}

func TestParseMeminfo(t *testing.T) {
	mem := "MemTotal:       16384000 kB\nMemFree:  8000000 kB\nMemAvailable:   12000000 kB\n"
	total, avail, err := parseMeminfo(strings.NewReader(mem))
	if err != nil {
		t.Fatalf("parseMeminfo: %v", err)
	}
	if total != 16384000*1024 {
		t.Errorf("total = %d, want %d", total, 16384000*1024)
	}
	if avail != 12000000*1024 {
		t.Errorf("avail = %d, want %d", avail, 12000000*1024)
	}

	// MemAvailable absent (old kernel): total still parses, avail is 0.
	total2, avail2, err := parseMeminfo(strings.NewReader("MemTotal: 100 kB\nMemFree: 50 kB\n"))
	if err != nil {
		t.Fatalf("parseMeminfo without MemAvailable: %v", err)
	}
	if total2 != 100*1024 || avail2 != 0 {
		t.Errorf("got total=%d avail=%d, want total=%d avail=0", total2, avail2, 100*1024)
	}

	if _, _, err := parseMeminfo(strings.NewReader("MemFree: 50 kB\n")); err == nil {
		t.Error("expected an error when MemTotal is absent")
	}
}

func TestCPUPercent(t *testing.T) {
	t0 := time.Now()
	// 1000 total jiffies elapse, 250 of them idle -> 75% busy.
	prev := Sample{CPUTotal: 10000, CPUIdle: 6000, At: t0}
	cur := Sample{CPUTotal: 11000, CPUIdle: 6250, At: t0.Add(time.Second)}
	got := CPUPercent(prev, cur)
	if want := 75.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("CPUPercent = %v, want %v", got, want)
	}
}

func TestCPUPercentHandlesReset(t *testing.T) {
	t0 := time.Now()
	prev := Sample{CPUTotal: 5000, CPUIdle: 4000, At: t0}
	cur := Sample{CPUTotal: 10, CPUIdle: 5, At: t0.Add(time.Second)} // counters went backwards
	if got := CPUPercent(prev, cur); got != 0 {
		t.Errorf("CPUPercent on a backward counter = %v, want 0", got)
	}
}

func TestReadRealProc(t *testing.T) {
	s, err := Read()
	if err != nil {
		t.Skipf("Read failed (not Linux?): %v", err)
	}
	if s.CPUTotal == 0 {
		t.Errorf("CPUTotal = %d, want positive", s.CPUTotal)
	}
	if s.MemTotalBytes <= 0 {
		t.Errorf("MemTotalBytes = %d, want positive", s.MemTotalBytes)
	}
	if s.MemUsedBytes < 0 || s.MemUsedBytes > s.MemTotalBytes {
		t.Errorf("MemUsedBytes = %d, want within [0, %d]", s.MemUsedBytes, s.MemTotalBytes)
	}
}
