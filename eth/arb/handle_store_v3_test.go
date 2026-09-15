package arb

import (
	"testing"
	"time"
)

func TestHandleV3ExpiryWaitsForBorrowAndDrainsBothStates(t *testing.T) {
	now := time.Unix(1000, 0)
	s := NewHandleStore(func() time.Time { return now }, counterIDs())
	p := s.Pin(idn("a"), time.Second)
	c, err := s.CreateChild(p, idn("a"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Borrow(c, idn("a")); err != nil {
		t.Fatal(err)
	}
	s.ReleaseHandle(p)
	now = now.Add(2 * time.Second)
	s.Expire()
	if len(s.DrainReleased()) != 0 {
		t.Fatal("borrowed state released")
	}
	s.ReleaseBorrow(c)
	released := s.DrainReleased()
	parents := 0
	for _, r := range released {
		if r.Parent {
			parents++
		}
	}
	if len(released) != 2 || parents != 1 || s.Size() != 0 {
		t.Fatalf("release cascade: %v", released)
	}
	if len(s.DrainReleased()) != 0 {
		t.Fatal("double release")
	}
}

func TestHandleV3AbandonedParentsAreEvicted(t *testing.T) {
	now := time.Unix(1000, 0)
	s := NewHandleStore(func() time.Time { return now }, counterIDs())
	s.Pin(idn("a"), time.Second)
	now = now.Add(time.Second)
	s.Expire()
	released := s.DrainReleased()
	if len(released) != 1 || !released[0].Parent || s.Size() != 0 {
		t.Fatal("unused parent leaked")
	}
}
