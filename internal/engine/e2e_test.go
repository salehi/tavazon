//go:build e2e

// End-to-end smoke test (docs/plans/phase-6.md §6.6). Opt-in behind the "e2e"
// build tag: it constructs the full stack — config, state, geoip, targets,
// uploader, metering, metrics, engine — pointed at a real loopback UDP
// listener, runs a few cycles, and proves traffic actually flowed.
//
//	go test -race -mod=vendor -tags e2e ./...
package engine

import (
	"context"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"tavazon/internal/config"
	"tavazon/internal/geoip"
	"tavazon/internal/metering"
	"tavazon/internal/metrics"
	"tavazon/internal/netstat"
	"tavazon/internal/schedule"
	"tavazon/internal/state"
	"tavazon/internal/targets"
	"tavazon/internal/uploader"
)

func TestEndToEnd(t *testing.T) {
	// 1. A real UDP listener on loopback.
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port

	var received atomic.Int64
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			received.Add(int64(n))
		}
	}()

	// 2. A one-ASN GeoIP graph whose single prefix is exactly the loopback
	//    host, so every manufactured datagram lands on the listener.
	const testASN = 64512
	_, prefix, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	g := geoip.NewForTest(
		map[uint32][]net.IPNet{testASN: {*prefix}},
		map[uint32]string{testASN: "IR"},
	)

	// 3. The full stack, pointed at that fixture, web disabled.
	dir := t.TempDir()
	cfg := config.Default()
	cfg.General.IntervalMin = config.Duration(time.Millisecond)
	cfg.General.IntervalMax = config.Duration(time.Millisecond)
	cfg.Target.Ratio.MinDeficitBytes = 0
	cfg.GeoIP.SelectedASNs = []uint32{testASN}
	cfg.Targets.PortMin = port // pin the port range to the listener
	cfg.Targets.PortMax = port
	for i := range cfg.Curve.Anchors {
		cfg.Curve.Anchors[i] = 2.0 // flat-high curve → worker count stays > 0
	}
	cfg.Curve.Max = 5
	cfg.State.File = filepath.Join(dir, "state.json")
	cfgFn := func() *config.Config { return cfg }

	st, err := state.Load(cfg.State.File)
	if err != nil {
		t.Fatal(err)
	}
	tg, _, err := targets.New(g, cfg.GeoIP.SelectedASNs, cfg.Targets, rand.New(rand.NewPCG(1, 2)))
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	store, err := metering.Open(config.MeteringConfig{
		Dir:           filepath.Join(dir, "metering"),
		Retention5Min: config.Duration(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := metrics.New()
	up := uploader.New(tg, reg, store, cfgFn, discardLogger(), rand.New(rand.NewPCG(3, 4)))
	curve := schedule.NewCurve(cfg.Curve, rand.New(rand.NewPCG(5, 6)))
	eng := New(cfgFn, st, curve, up, store, reg, discardLogger(), rand.New(rand.NewPCG(7, 8)))

	// Fake counters: download dwarfs upload so the ratio deficit stays small
	// but positive every cycle, forcing the uploader to run.
	eng.netstat = func(string) (netstat.Counters, error) {
		return netstat.Counters{TxBytes: 0, RxBytes: 2000}, nil
	}
	eng.sysstat = fakeSysstat

	// 4. Run a few short cycles.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	// 5. Assertions.
	if received.Load() == 0 {
		t.Error("loopback listener received no datagrams")
	}
	if got := reg.Snapshot().FakeBytesTotal; got == 0 {
		t.Error("metrics fake_bytes_total did not advance")
	}
	buckets, err := store.History(time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var asnBytes int64
	for _, b := range buckets {
		asnBytes += b.PerASN[testASN]
	}
	if asnBytes == 0 {
		t.Errorf("metering recorded no bytes against AS%d", testASN)
	}
	if fi, err := os.Stat(cfg.State.File); err != nil || fi.Size() == 0 {
		t.Errorf("state.json was not written: stat err=%v", err)
	}
}
