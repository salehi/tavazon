package metering

import (
	"math"
	"sort"
	"time"
)

// Billing summarises what a backbone provider charges on: the p-th-percentile
// rate of 5-minute samples and the total transferred volume, over a window.
type Billing struct {
	UpP95BPS       float64 `json:"up_p95_bps"`
	DownP95BPS     float64 `json:"down_p95_bps"`
	UpTotalBytes   int64   `json:"up_total_bytes"`
	DownTotalBytes int64   `json:"down_total_bytes"`
	Percentile     int     `json:"percentile"`
	Samples        int     `json:"samples"`
}

// nthPercentile returns the p-th percentile of vals (p in 1..100) by the
// "drop the top (100-p)%" rule: the highest value still inside the kept set.
func nthPercentile(vals []float64, p int) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	idx := int(math.Ceil(float64(p)/100*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// Billing computes the p-th-percentile rate and total volume over [from, to]
// from the recorded 5-minute buckets.
func (s *Store) Billing(from, to time.Time, p int) (Billing, error) {
	buckets, err := s.History(from, to)
	if err != nil {
		return Billing{}, err
	}
	secs := bucketDuration.Seconds()
	ups := make([]float64, 0, len(buckets))
	downs := make([]float64, 0, len(buckets))
	b := Billing{Percentile: p, Samples: len(buckets)}
	for i := range buckets {
		ups = append(ups, float64(buckets[i].UpBytes)/secs)
		downs = append(downs, float64(buckets[i].DownBytes)/secs)
		b.UpTotalBytes += buckets[i].UpBytes
		b.DownTotalBytes += buckets[i].DownBytes
	}
	b.UpP95BPS = nthPercentile(ups, p)
	b.DownP95BPS = nthPercentile(downs, p)
	return b, nil
}
