// Per-parent-root cache for the V3 deep read (slot0/liquidity/tickSpacing/
// bitmap window/ticks/fee). It exists so repeated arb_getPostPoolState calls on
// the same parent block reuse canonical pool state instead of re-reading it.
//
// Correctness: a pool's canonical state is fixed within one parent state root.
// A post-target handle may have written some pools; those accounts appear in the
// borrowed StateDB's GetDirtyAccounts() set and are never cached or served from
// the cache. Keys include the parent state root, so an entry can never leak
// across blocks. ARB_V3_CACHE_MODE = off | observe | on (default observe).
package eth

import (
	"os"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/arb"
)

type v3CacheMode int

const (
	v3CacheOff v3CacheMode = iota
	v3CacheObserve
	v3CacheOn
)

func v3CacheModeFromEnv() v3CacheMode {
	switch os.Getenv("ARB_V3_CACHE_MODE") {
	case "on":
		return v3CacheOn
	case "off":
		return v3CacheOff
	default:
		return v3CacheObserve
	}
}

var v3CacheModeSetting = v3CacheModeFromEnv()

type v3CacheEntry struct {
	snaps map[common.Address]arb.PoolSnapshot
}

type v3FullCache struct {
	mu       sync.Mutex
	maxRoots int
	order    []common.Hash
	roots    map[common.Hash]*v3CacheEntry
	hits     uint64
	misses   uint64
}

func newV3FullCache(maxRoots int) *v3FullCache {
	if maxRoots < 1 {
		maxRoots = 1
	}
	return &v3FullCache{maxRoots: maxRoots, roots: make(map[common.Hash]*v3CacheEntry, maxRoots)}
}

func (c *v3FullCache) get(root common.Hash, pool common.Address) (arb.PoolSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.roots[root]
	if !ok {
		return arb.PoolSnapshot{}, false
	}
	snap, ok := entry.snaps[pool]
	return snap, ok
}

func (c *v3FullCache) put(root common.Hash, pool common.Address, snap arb.PoolSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.roots[root]
	if !ok {
		entry = &v3CacheEntry{snaps: make(map[common.Address]arb.PoolSnapshot)}
		c.roots[root] = entry
		c.order = append(c.order, root)
		for len(c.order) > c.maxRoots {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.roots, oldest)
		}
	}
	entry.snaps[pool] = snap
}

// Bounded by maxRoots parent blocks; each entry is one arb.PoolSnapshot per V3
// pool read in that block. With 4 roots and even 500 V3 pools this stays within
// a few MB, negligible against the node's state cache.
var v3PoolCache = newV3FullCache(4)

type v3CacheJobStats struct {
	hits   uint64
	misses uint64
}

func (s *v3CacheJobStats) snapshot() (uint64, uint64) {
	return atomic.LoadUint64(&s.hits), atomic.LoadUint64(&s.misses)
}
