package metrics

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAddFakeBytesConcurrent(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				r.AddFakeBytes(1)
			}
		}()
	}
	wg.Wait()
	if got := r.Snapshot().FakeBytesTotal; got != 50000 {
		t.Errorf("FakeBytesTotal = %d, want 50000", got)
	}
}

func TestObserveCountersBPS(t *testing.T) {
	r := New()
	t0 := time.Now()
	r.ObserveCounters(t0, 1000, 2000)
	r.ObserveCounters(t0.Add(10*time.Second), 1000+20000, 2000+10000)
	s := r.Snapshot()
	if s.UploadBPS != 2000 {
		t.Errorf("UploadBPS = %d, want 2000", s.UploadBPS)
	}
	if s.DownloadBPS != 1000 {
		t.Errorf("DownloadBPS = %d, want 1000", s.DownloadBPS)
	}
	if s.TrackedUpload != 21000 {
		t.Errorf("TrackedUpload = %d, want 21000", s.TrackedUpload)
	}
}

func TestObserveCountersHandlesReset(t *testing.T) {
	r := New()
	t0 := time.Now()
	r.ObserveCounters(t0, 1_000_000, 0)
	r.ObserveCounters(t0.Add(time.Second), 5, 0) // counter went backwards (reboot)
	if bps := r.Snapshot().UploadBPS; bps < 0 {
		t.Errorf("UploadBPS = %d, want clamped to >= 0 on a counter reset", bps)
	}
}

func TestStartCycleResets(t *testing.T) {
	r := New()
	r.AddFakeBytes(500)
	if r.Snapshot().FakeBytesCycle != 500 {
		t.Fatal("FakeBytesCycle should be 500")
	}
	r.StartCycle()
	if got := r.Snapshot().FakeBytesCycle; got != 0 {
		t.Errorf("after StartCycle, FakeBytesCycle = %d, want 0", got)
	}
	if r.Snapshot().FakeBytesTotal != 500 {
		t.Error("StartCycle must not reset FakeBytesTotal")
	}
}

func TestPrometheus(t *testing.T) {
	r := New()
	r.AddFakeBytes(12345)
	r.SetDeficit(999)
	out := r.Prometheus()
	for _, want := range []string{
		"tavazon_fake_bytes_total 12345",
		"tavazon_deficit_bytes 999",
		"tavazon_uptime_seconds",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Prometheus output is missing %q:\n%s", want, out)
		}
	}
}
