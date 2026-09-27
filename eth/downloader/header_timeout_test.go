package downloader

import (
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

// Embedding Peer leaves unused methods unavailable; both header methods below
// emulate a connected peer that accepts requests but never replies.
type silentHeaderPeer struct{ Peer }

func (silentHeaderPeer) RequestHeadersByHash(common.Hash, int, int, bool, chan *eth.Response) (*eth.Request, error) {
	return &eth.Request{}, nil
}

func (silentHeaderPeer) RequestHeadersByNumber(uint64, int, int, bool, chan *eth.Response) (*eth.Request, error) {
	return &eth.Request{}, nil
}

func TestHeaderTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 5 * time.Second}, {"0", 0}, {"1000", time.Second},
		{"60000", time.Minute}, {"999", 5 * time.Second},
		{"60001", 5 * time.Second}, {"-1", 5 * time.Second},
		{"18446744073709551616", 5 * time.Second}, {"bad", 5 * time.Second},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("ARB_SYNC_HEADER_TIMEOUT_MS", tc.value)
			if got := configuredHeaderTimeout(); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHeaderRequestsTimeoutAndCancel(t *testing.T) {
	for _, byHash := range []bool{true, false} {
		for _, cancel := range []bool{false, true} {
			d := &Downloader{peers: newPeerSet(), headerTimeoutCap: 20 * time.Millisecond, cancelCh: make(chan struct{})}
			p := newPeerConnection("silent", 68, silentHeaderPeer{}, log.New())
			want := errTimeout
			if cancel {
				close(d.cancelCh)
				want = errCanceled
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if byHash {
					_, _, err = d.fetchHeadersByHash(p, common.Hash{}, 1, 0, false)
				} else {
					_, _, err = d.fetchHeadersByNumber(p, 1, 1, 0, false)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("hash=%v cancel=%v: got %v, want %v", byHash, cancel, err, want)
				}
			case <-time.After(time.Second):
				t.Fatalf("hash=%v cancel=%v: request did not stop", byHash, cancel)
			}
		}
	}
}

func TestHeaderTimeoutDoesNotChangeOtherRequests(t *testing.T) {
	d := &Downloader{peers: newPeerSet(), headerTimeoutCap: 5 * time.Second}
	adaptive := d.peers.rates.TargetTimeout()
	if got := d.headerRequestTimeout(); got != min(adaptive, 5*time.Second) {
		t.Fatalf("unexpected header timeout: %v", got)
	}
	if got := d.peers.rates.TargetTimeout(); got != adaptive {
		t.Fatalf("shared adaptive timeout changed from %v to %v", adaptive, got)
	}
	d.headerTimeoutCap = 0
	if got := d.headerRequestTimeout(); got != adaptive {
		t.Fatalf("disabled cap changed adaptive timeout: %v", got)
	}
}
