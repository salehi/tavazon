package metering

import (
	"testing"
	"time"

	"tavazon/internal/config"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(config.MeteringConfig{
		Dir:           t.TempDir(),
		Retention5Min: config.Duration(9000 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestSampleBucketsAndFlushes(t *testing.T) {
	s := openStore(t)
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	if err := s.Sample(base, 1000, 500); err != nil { // baseline
		t.Fatal(err)
	}
	if err := s.Sample(base.Add(2*time.Minute), 4000, 1500); err != nil { // +3000/+1000, same bucket
		t.Fatal(err)
	}
	if err := s.Sample(base.Add(6*time.Minute), 4000, 1500); err != nil { // new bucket -> flush 12:00
		t.Fatal(err)
	}
	hist, err := s.History(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	var found *Bucket
	for i := range hist {
		if hist[i].Start.Equal(base) {
			found = &hist[i]
		}
	}
	if found == nil {
		t.Fatalf("12:00 bucket missing from history %+v", hist)
	}
	if found.UpBytes != 3000 || found.DownBytes != 1000 {
		t.Errorf("12:00 bucket = up %d down %d, want 3000/1000", found.UpBytes, found.DownBytes)
	}
}

func TestRecordSendPerASN(t *testing.T) {
	s := openStore(t)
	s.RecordSend(100, 5000)
	s.RecordSend(200, 3000)
	s.RecordSend(100, 2000)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	hist, err := s.History(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	var fake, asn100, asn200 int64
	for _, b := range hist {
		fake += b.FakeBytes
		asn100 += b.PerASN[100]
		asn200 += b.PerASN[200]
	}
	if fake != 10000 || asn100 != 7000 || asn200 != 3000 {
		t.Errorf("totals: fake %d asn100 %d asn200 %d, want 10000/7000/3000", fake, asn100, asn200)
	}
}

func TestNthPercentile(t *testing.T) {
	vals := make([]float64, 100)
	for i := range vals {
		vals[i] = float64(i + 1)
	}
	if got := nthPercentile(vals, 95); got != 95 {
		t.Errorf("nthPercentile(1..100, 95) = %v, want 95", got)
	}
	if got := nthPercentile(vals, 100); got != 100 {
		t.Errorf("nthPercentile(1..100, 100) = %v, want 100", got)
	}
	if got := nthPercentile(vals, 50); got != 50 {
		t.Errorf("nthPercentile(1..100, 50) = %v, want 50", got)
	}
	if got := nthPercentile(nil, 95); got != 0 {
		t.Errorf("nthPercentile(nil, 95) = %v, want 0", got)
	}
}

func TestBilling(t *testing.T) {
	s := openStore(t)
	base := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	up := int64(1000)
	for i := 0; i < 12; i++ {
		if err := s.Sample(base.Add(time.Duration(i)*6*time.Minute), up, 0); err != nil {
			t.Fatal(err)
		}
		up += 1_000_000
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := s.Billing(base.Add(-time.Hour), base.Add(3*time.Hour), 95)
	if err != nil {
		t.Fatalf("Billing: %v", err)
	}
	if b.UpTotalBytes <= 0 {
		t.Errorf("UpTotalBytes = %d, want positive", b.UpTotalBytes)
	}
	if b.UpP95BPS <= 0 {
		t.Errorf("UpP95BPS = %v, want positive", b.UpP95BPS)
	}
}

func TestAudit(t *testing.T) {
	s := openStore(t)
	for i := 0; i < 5; i++ {
		if err := s.AppendAudit(AuditRecord{Source: "api", Change: "edit"}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	all, err := s.Audit(0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("Audit(0) = %d records, want 5", len(all))
	}
	if last3, err := s.Audit(3); err != nil || len(last3) != 3 {
		t.Errorf("Audit(3) = %d records (err %v), want 3", len(last3), err)
	}
}
