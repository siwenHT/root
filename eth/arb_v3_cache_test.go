package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/arb"
)

func v3snap(pool common.Address, liq string) arb.PoolSnapshot {
	return arb.PoolSnapshot{Locator: pool.Hex(), Kind: "v3", Liquidity: liq}
}

func TestV3FullCacheHitMissAndRootEviction(t *testing.T) {
	c := newV3FullCache(2)
	pool := common.HexToAddress("0x01")
	r1 := common.HexToHash("0xaa")
	r2 := common.HexToHash("0xbb")
	r3 := common.HexToHash("0xcc")
	if _, ok := c.get(r1, pool); ok {
		t.Fatal("unexpected hit on empty cache")
	}
	c.put(r1, pool, v3snap(pool, "1"))
	if s, ok := c.get(r1, pool); !ok || s.Liquidity != "1" {
		t.Fatalf("expected r1 hit, got ok=%v snap=%v", ok, s)
	}
	c.put(r2, pool, v3snap(pool, "2"))
	c.put(r3, pool, v3snap(pool, "3"))
	if _, ok := c.get(r1, pool); ok {
		t.Fatal("r1 should be evicted by the 2-root bound")
	}
	if s, ok := c.get(r2, pool); !ok || s.Liquidity != "2" {
		t.Fatal("r2 should stay resident")
	}
	if s, ok := c.get(r3, pool); !ok || s.Liquidity != "3" {
		t.Fatal("r3 should be resident")
	}
}

func TestV3FullCacheKeysByBothRootAndPool(t *testing.T) {
	c := newV3FullCache(4)
	a := common.HexToAddress("0x01")
	b := common.HexToAddress("0x02")
	r := common.HexToHash("0xaa")
	c.put(r, a, v3snap(a, "10"))
	if _, ok := c.get(r, b); ok {
		t.Fatal("different pool must not hit")
	}
	if _, ok := c.get(common.HexToHash("0xbb"), a); ok {
		t.Fatal("different root must not hit")
	}
}
