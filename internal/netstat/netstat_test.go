package netstat

import (
	"os"
	"testing"
)

func openFixture(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open("testdata/proc_net_dev")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestParseSumExcludesLoopback(t *testing.T) {
	c, err := parse(openFixture(t), "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wantRx := uint64(5000000 + 2000000)
	wantTx := uint64(9000000 + 3000000)
	if c.RxBytes != wantRx || c.TxBytes != wantTx {
		t.Errorf("auto-sum = rx %d tx %d, want rx %d tx %d", c.RxBytes, c.TxBytes, wantRx, wantTx)
	}
}

func TestParseNamedInterface(t *testing.T) {
	c, err := parse(openFixture(t), "eth0")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.RxBytes != 5000000 || c.TxBytes != 9000000 {
		t.Errorf("eth0 = rx %d tx %d, want 5000000/9000000", c.RxBytes, c.TxBytes)
	}
}

func TestParseLoopbackSelectableByName(t *testing.T) {
	c, err := parse(openFixture(t), "lo")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.RxBytes != 1000 || c.TxBytes != 1000 {
		t.Errorf("lo = rx %d tx %d, want 1000/1000", c.RxBytes, c.TxBytes)
	}
}

func TestParseUnknownInterface(t *testing.T) {
	if _, err := parse(openFixture(t), "wlan9"); err == nil {
		t.Fatal("expected an error for an unknown interface")
	}
}
