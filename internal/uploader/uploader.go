// Package uploader runs the bounded worker pool and token-bucket rate limiter
// that send randomized junk UDP datagrams. See docs/project.md §6.8, §6.9, §7.8.
package uploader

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/salehi/tavazon/internal/config"
	"github.com/salehi/tavazon/internal/metering"
	"github.com/salehi/tavazon/internal/metrics"
	"github.com/salehi/tavazon/internal/targets"
)

// Uploader manufactures junk UDP traffic with a bounded worker pool.
type Uploader struct {
	targets  *targets.Targets
	metrics  *metrics.Registry
	metering *metering.Store
	cfg      func() *config.Config
	log      *slog.Logger
	rng      *rand.Rand
}

// New builds an Uploader. cfg is a closure so live config reloads (including
// the dev-mode toggle) are picked up on each cycle.
func New(tg *targets.Targets, m *metrics.Registry, store *metering.Store, cfg func() *config.Config, log *slog.Logger, rng *rand.Rand) *Uploader {
	return &Uploader{targets: tg, metrics: m, metering: store, cfg: cfg, log: log, rng: rng}
}

// RunCycle manufactures up to budget bytes spread over the given number of
// workers (capped at MaxWorkers). It blocks until done or ctx is cancelled,
// and returns the bytes actually sent.
func (u *Uploader) RunCycle(ctx context.Context, budget int64, workers int) int64 {
	uc := u.cfg().Uploader
	if workers < 1 {
		workers = 1
	}
	if workers > uc.MaxWorkers {
		workers = uc.MaxWorkers
	}
	if budget < 0 {
		budget = 0
	}
	limiter := newTokenBucket(float64(uc.BaseRateBPS) * float64(uc.SpeedCoefficient))
	perWorker := budget / int64(workers)

	var sent atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		seed1, seed2 := u.rng.Uint64(), u.rng.Uint64()
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					u.log.Error("uploader: worker panic recovered", "panic", r)
				}
			}()
			rng := rand.New(rand.NewPCG(seed1, seed2))
			u.runWorker(ctx, limiter, rng, jitterQuota(perWorker, rng), &sent)
		}()
	}
	wg.Wait()
	return sent.Load()
}

// jitterQuota perturbs a worker's byte quota by +/-20%.
func jitterQuota(base int64, rng *rand.Rand) int64 {
	if base <= 0 {
		return 0
	}
	return int64(float64(base) * (0.8 + rng.Float64()*0.4))
}

func (u *Uploader) runWorker(ctx context.Context, limiter *tokenBucket, rng *rand.Rand, quota int64, sent *atomic.Int64) {
	if quota <= 0 {
		return
	}
	uc := u.cfg().Uploader
	ip, port, asn := u.pickTarget(rng)
	if ip == nil {
		return
	}
	// An unconnected socket: per-destination ICMP port-unreachable is not
	// delivered, so writes do not start failing against a dead port (the
	// common case for junk traffic).
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		u.log.Debug("uploader: open socket failed", "err", err)
		return
	}
	defer conn.Close()
	dst := &net.UDPAddr{IP: ip, Port: port}

	buf := make([]byte, uc.MaxDatagram)
	gap := uc.PacketGapMax.Std()
	var local int64
	for local < quota {
		if ctx.Err() != nil {
			return
		}
		size := RandomSize(uc, rng)
		Random(buf[:size], rng)
		if err := limiter.WaitN(ctx, size); err != nil {
			return // ctx cancelled
		}
		if gap > 0 {
			sleepJitter(ctx, rng, gap)
		}
		n, err := conn.WriteToUDP(buf[:size], dst)
		if err != nil {
			u.log.Debug("uploader: write failed", "dst", dst.String(), "err", err)
			return
		}
		local += int64(n)
		sent.Add(int64(n))
		u.metrics.AddFakeBytes(int64(n))
		u.metering.RecordSend(asn, int64(n))
	}
}

// pickTarget returns the destination for a worker: the fixed dev.target in dev
// mode (ASN 0 = synthetic "DEV"), otherwise a random ASN-based target.
func (u *Uploader) pickTarget(rng *rand.Rand) (net.IP, int, uint32) {
	cfg := u.cfg()
	if cfg.Dev.Enabled {
		return net.ParseIP(cfg.Dev.Target), randomPort(cfg.Targets, rng), 0
	}
	return u.targets.RandomTarget()
}

func randomPort(c config.TargetsConfig, rng *rand.Rand) int {
	if c.PortMax <= c.PortMin {
		return c.PortMin
	}
	return c.PortMin + rng.IntN(c.PortMax-c.PortMin+1)
}

func sleepJitter(ctx context.Context, rng *rand.Rand, max time.Duration) {
	d := time.Duration(rng.Int64N(int64(max) + 1))
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
	case <-timer.C:
	}
}
