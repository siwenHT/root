package legacypool

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

type preloadCountReader struct {
	state.Reader
	mu               sync.Mutex
	calls            map[common.Address]int
	err              error
	once             sync.Once
	entered, release chan struct{}
}

func (r *preloadCountReader) Account(addr common.Address) (*types.StateAccount, error) {
	if r.entered != nil {
		r.once.Do(func() { close(r.entered); <-r.release })
	}
	r.mu.Lock()
	r.calls[addr]++
	err := r.err
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return r.Reader.Account(addr)
}

func (r *preloadCountReader) count(addr common.Address) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[addr]
}

type preloadTestChain struct {
	*preloadChain
	readers  map[common.Hash]state.Reader
	prepared atomic.Int64
	opened   atomic.Int64
	prepare  func(common.Hash) (*state.StateDB, error)
}

func (c *preloadTestChain) StateAt(root common.Hash) (*state.StateDB, error) {
	c.opened.Add(1)
	if r, ok := c.readers[root]; ok {
		return state.NewWithReader(root, c.db, r)
	}
	return nil, errors.New("unknown root")
}

func (c *preloadTestChain) StateAtForPool(root common.Hash) (*state.StateDB, error) {
	c.prepared.Add(1)
	if c.prepare != nil {
		return c.prepare(root)
	}
	return c.StateAt(root)
}

func preloadKeys(n int) []*ecdsa.PrivateKey {
	keys := make([]*ecdsa.PrivateKey, n)
	for i := range keys {
		keys[i], _ = crypto.ToECDSA(common.LeftPadBytes([]byte{byte(i + 1)}, 32))
	}
	return keys
}

func preloadAccounts(keys []*ecdsa.PrivateKey, nonce uint64, balance uint64) *preloadReader {
	r := &preloadReader{accounts: make(map[common.Hash][]byte)}
	for _, key := range keys {
		addr := crypto.PubkeyToAddress(key.PublicKey)
		r.accounts[crypto.Keccak256Hash(addr[:])] = types.SlimAccountRLP(types.StateAccount{
			Nonce: nonce, Balance: uint256.NewInt(balance), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash.Bytes()})
	}
	return r
}

func newPreloadTestPool(t *testing.T, readers map[common.Hash]state.Reader, enabled bool) (*LegacyPool, *preloadTestChain) {
	t.Helper()
	db := state.NewDatabaseForTesting()
	base, _ := state.New(types.EmptyRootHash, db)
	for _, reader := range readers {
		if r, ok := reader.(*preloadReader); ok {
			r.Reader = base.Reader()
		}
		if r, ok := reader.(*preloadCountReader); ok {
			if inner, ok := r.Reader.(*preloadReader); ok {
				inner.Reader = base.Reader()
			}
		}
	}
	chain := &preloadTestChain{preloadChain: &preloadChain{BlockChain: newTestBlockChain(params.MergedTestChainConfig, 30000000, base, nil), db: db}, readers: readers}
	var provider BlockChain = chain
	if !enabled {
		provider = struct{ BlockChain }{chain}
	}
	pool := New(testTxPoolConfig, provider)
	if err := pool.Init(1, chain.CurrentBlock(), newReserver()); err != nil {
		t.Fatal(err)
	}
	<-pool.initDoneCh
	return pool, chain
}

func preloadWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}
}

func TestPreloadBundleOwnershipAndEarlyNoncer(t *testing.T) {
	keys := preloadKeys(2)
	r := &preloadCountReader{Reader: preloadAccounts(keys, 7, params.Ether), calls: make(map[common.Address]int)}
	root := common.HexToHash("01")
	pool, chain := newPreloadTestPool(t, map[common.Hash]state.Reader{{}: preloadAccounts(keys, 0, params.Ether), root: r}, true)
	defer pool.Close()
	addr := crypto.PubkeyToAddress(keys[0].PublicKey)
	pool.mu.Lock()
	pool.pending[addr] = newList(true)
	pool.queue.queued[addr] = newList(false)
	pool.mu.Unlock()
	events := map[common.Address]*SortedMap{addr: NewSortedMap()}
	old := pool.currentHead.Load()
	head := preloadHead(old, root)
	p := pool.preloadReset(&txpoolResetRequest{old, head}, events)
	if p == nil || p.state == pool.currentState || p.nonces.fallback == p.state || chain.prepared.Load() != 1 {
		t.Fatal("exclusive preparation not used")
	}
	if got := r.count(addr); got != 1 {
		t.Fatalf("duplicate snapshot address loaded %d times", got)
	}
	if p.state.GetBalance(addr).Uint64() != params.Ether || r.count(addr) != 1 {
		t.Fatal("balance was not warmed")
	}
	if p.nonces.get(addr) != 7 || r.count(addr) != 2 {
		t.Fatal("noncer was copied after warmup")
	}
	pool.mu.Lock()
	pool.resetWithPreload(old, head, p)
	if pool.currentState != p.state || pool.pendingNonces != p.nonces {
		t.Fatal("bundle not adopted intact")
	}
	pool.mu.Unlock()
	if err := pool.addRemoteSync(transaction(0, 21000, keys[1])); !errors.Is(err, core.ErrNonceTooLow) {
		t.Fatalf("old view accepted after reset: %v", err)
	}
}

func TestPreloadFallbacks(t *testing.T) {
	for _, reason := range []string{"unsupported", "nil_head", "same_height", "reorg", "deep_reorg", "wrong_old", "clear", "changed_state", "changed_target", "open_error", "nil_state", "shared_state", "read_error"} {
		t.Run(reason, func(t *testing.T) {
			keys := preloadKeys(1)
			root := common.HexToHash("02")
			r := &preloadCountReader{Reader: preloadAccounts(keys, 1, params.Ether), calls: make(map[common.Address]int)}
			pool, chain := newPreloadTestPool(t, map[common.Hash]state.Reader{{}: preloadAccounts(keys, 0, params.Ether), root: r}, reason != "unsupported")
			defer pool.Close()
			if err := pool.addRemoteSync(transaction(0, 21000, keys[0])); err != nil {
				t.Fatal(err)
			}
			old := pool.currentHead.Load()
			head := preloadHead(old, root)
			req := &txpoolResetRequest{old, head}
			switch reason {
			case "nil_head":
				req.oldHead = nil
			case "same_height":
				head.Number.Set(old.Number)
			case "reorg":
				head.ParentHash = common.HexToHash("dead")
			case "deep_reorg":
				head.Number.SetUint64(100)
			case "wrong_old":
				req.oldHead = types.CopyHeader(old)
				req.oldHead.Extra = []byte{1}
				head.ParentHash = req.oldHead.Hash()
			case "open_error":
				chain.prepare = func(common.Hash) (*state.StateDB, error) { return nil, errors.New("unavailable") }
			case "nil_state":
				chain.prepare = func(common.Hash) (*state.StateDB, error) { return nil, nil }
			case "shared_state":
				chain.prepare = func(common.Hash) (*state.StateDB, error) { return pool.currentState, nil }
			case "read_error":
				r.err = errors.New("reader failed")
			}
			p := pool.preloadReset(req, nil)
			if reason == "clear" {
				pool.Clear()
			}
			if reason == "changed_state" {
				pool.mu.Lock()
				pool.currentState = pool.currentState.Copy()
				pool.mu.Unlock()
			}
			if reason == "changed_target" {
				head.Extra = []byte{1}
			}
			pool.mu.Lock()
			valid := p.valid(pool, req.oldHead, head)
			pool.mu.Unlock()
			if valid {
				t.Fatal("invalid preparation accepted")
			}
			if reason == "clear" || reason == "changed_state" || reason == "changed_target" {
				if p == nil {
					t.Fatal("invalidation case never prepared")
				}
			}
			// Restore valid target requests and healthy reader for fallback; no
			// error-produced zero balance/nonce may be installed from preparation.
			r.err = nil
			before := chain.opened.Load()
			pool.mu.Lock()
			pool.resetWithPreload(old, head, p)
			pool.mu.Unlock()
			if chain.opened.Load() != before+1 {
				t.Fatal("original StateAt fallback not executed")
			}
			if pool.currentState == nil {
				t.Fatal("missing fallback state")
			}
		})
	}
}

func preloadPoolView(pool *LegacyPool) []string {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	var out []string
	for addr, l := range pool.pending {
		for _, tx := range l.Flatten() {
			out = append(out, fmt.Sprintf("pending/%s/%s", addr, tx.Hash()))
		}
	}
	for addr, l := range pool.queue.queued {
		for _, tx := range l.Flatten() {
			out = append(out, fmt.Sprintf("queued/%s/%s", addr, tx.Hash()))
		}
	}
	for hash := range pool.all.txs {
		out = append(out, "all/"+hash.Hex())
	}
	for addr, nonce := range pool.pendingNonces.nonces {
		out = append(out, fmt.Sprintf("nonce/%s/%d", addr, nonce))
	}
	r := pool.reserver.(*reserver)
	r.lock.Lock()
	for addr := range r.accounts {
		out = append(out, "reserved/"+addr.Hex())
	}
	r.lock.Unlock()
	// Unique prices in these fixtures make eviction sets independent of map
	// iteration. Heap layout itself is intentionally not a total ordering.
	pool.priced.Reheap()
	for _, tx := range pool.priced.urgent.list {
		out = append(out, "urgent/"+tx.Hash().Hex())
	}
	for _, tx := range pool.priced.floating.list {
		out = append(out, "floating/"+tx.Hash().Hex())
	}
	drops, ok := pool.priced.Discard(2)
	out = append(out, fmt.Sprint("discard_ok/", ok))
	for _, tx := range drops {
		out = append(out, "discard/"+tx.Hash().Hex())
	}
	sort.Strings(out)
	return out
}

func TestPreloadResetDifferential(t *testing.T) {
	for _, tc := range []struct {
		name           string
		nonce, balance uint64
		gas            uint64
		missing        bool
	}{
		{"unchanged", 0, params.Ether, 30000000, false}, {"nonce_forward", 1, params.Ether, 30000000, false},
		{"nonce_rollback", 0, params.Ether, 30000000, false}, {"balance_drop_internal_transfer", 0, 1000, 30000000, false},
		{"balance_recovery", 0, params.Ether, 30000000, false}, {"gas_limit", 0, params.Ether, 21000, false},
		{"missing", 0, 0, 30000000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := preloadKeys(4)
			root := common.HexToHash("03")
			var views [][]string
			var notifications [][]string
			for _, enabled := range []bool{false, true} {
				initial := preloadAccounts(keys, 0, params.Ether)
				if tc.name == "nonce_rollback" {
					initial = preloadAccounts(keys, 1, params.Ether)
				}
				target := preloadAccounts(keys, tc.nonce, tc.balance)
				if tc.missing {
					target = preloadAccounts(nil, 0, 0)
				}
				pool, chain := newPreloadTestPool(t, map[common.Hash]state.Reader{{}: initial, root: target}, enabled)
				ch := make(chan core.NewTxsEvent, 20)
				sub := pool.SubscribeTransactions(ch, false)
				for i, key := range keys {
					for _, n := range []uint64{0, 1, 3} {
						if tc.name == "nonce_rollback" {
							n++
						}
						tx := pricedTransaction(n, 22000, big.NewInt(int64(10+i)), key)
						if err := pool.addRemoteSync(tx); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Exercise same-nonce replacement before the head transition.
				n := uint64(0)
				if tc.name == "nonce_rollback" {
					n = 1
				}
				if err := pool.addRemoteSync(pricedTransaction(n, 22000, big.NewInt(50), keys[0])); err != nil {
					t.Fatal(err)
				}
				for len(ch) > 0 {
					<-ch
				}
				old := pool.currentHead.Load()
				head := preloadHead(old, root)
				head.GasLimit = tc.gas
				pool.Reset(old, head)
				if enabled && chain.prepared.Load() != 1 {
					t.Fatal("differential silently skipped fast path")
				}
				if err := validatePoolInternals(pool); err != nil {
					t.Fatal(err)
				}
				var got []string
				for len(ch) > 0 {
					for _, tx := range (<-ch).Txs {
						got = append(got, tx.Hash().Hex())
					}
				}
				sort.Strings(got)
				notifications = append(notifications, got)
				views = append(views, preloadPoolView(pool))
				sub.Unsubscribe()
				pool.Close()
			}
			if !reflect.DeepEqual(views[0], views[1]) || !reflect.DeepEqual(notifications[0], notifications[1]) {
				t.Fatalf("baseline differs\n%v\n%v\nevents=%v", views[0], views[1], notifications)
			}
		})
	}
}

func TestPreloadConcurrentMutation(t *testing.T) {
	for _, action := range []string{"add", "replace", "remove", "clear", "next_heads", "close"} {
		t.Run(action, func(t *testing.T) {
			keys := preloadKeys(2)
			root := common.HexToHash("04")
			nextRoot := common.HexToHash("05")
			r := &preloadCountReader{Reader: preloadAccounts(keys, 1, params.Ether), calls: make(map[common.Address]int), entered: make(chan struct{}), release: make(chan struct{})}
			pool, chain := newPreloadTestPool(t, map[common.Hash]state.Reader{{}: preloadAccounts(keys, 0, params.Ether), root: r, nextRoot: preloadAccounts(keys, 2, params.Ether)}, true)
			closed := false
			defer func() {
				if !closed {
					pool.Close()
				}
			}()
			tx := transaction(0, 21000, keys[0])
			if err := pool.addRemoteSync(tx); err != nil {
				t.Fatal(err)
			}
			ch := make(chan core.NewTxsEvent, 8)
			sub := pool.SubscribeTransactions(ch, false)
			defer sub.Unsubscribe()
			old := pool.currentHead.Load()
			head := preloadHead(old, root)
			done := pool.requestReset(old, head)
			preloadWait(t, r.entered)
			oldState := pool.currentState
			var nextDone chan struct{}
			var closedCh chan struct{}
			switch action {
			case "add":
				if err := pool.Add([]*types.Transaction{transaction(0, 21000, keys[1])}, false)[0]; err != nil {
					t.Fatal(err)
				}
			case "replace":
				if err := pool.Add([]*types.Transaction{pricedTransaction(0, 21000, big.NewInt(2), keys[0])}, false)[0]; err != nil {
					t.Fatal(err)
				}
			case "remove":
				pool.mu.Lock()
				pool.removeTx(tx.Hash(), true, true)
				pool.mu.Unlock()
			case "clear":
				pool.Clear()
			case "next_heads":
				head2 := preloadHead(head, nextRoot)
				head3 := preloadHead(head2, nextRoot)
				nextDone = pool.requestReset(head, head2)
				if d := pool.requestReset(head2, head3); d != nextDone {
					t.Fatal("queued heads not coalesced")
				}
			case "close":
				closedCh = make(chan struct{})
				go func() { pool.Close(); close(closedCh) }()
			}
			if pool.currentHead.Load() != old || pool.currentState != oldState {
				t.Fatal("warmup published state early")
			}
			close(r.release)
			preloadWait(t, done)
			if nextDone != nil {
				preloadWait(t, nextDone)
				if pool.currentHead.Load().Number.Uint64() != 3 {
					t.Fatal("coalesced head lost")
				}
			}
			if closedCh != nil {
				preloadWait(t, closedCh)
				closed = true
			} else {
				preloadWait(t, pool.requestPromoteExecutables(newAccountSet(pool.signer)))
			}
			if len(ch) != 0 {
				t.Fatal("notification survived removal at the new head")
			}
			if pool.currentState == oldState {
				t.Fatal("head never installed")
			}
			if action == "clear" && chain.opened.Load() < 3 {
				t.Fatal("Clear did not invalidate candidate")
			}
			if action == "add" {
				addr := crypto.PubkeyToAddress(keys[1].PublicKey)
				pool.mu.Lock()
				nonce := pool.currentState.GetNonce(addr)
				pool.mu.Unlock()
				if r.count(addr) == 0 || nonce != 1 {
					t.Fatal("new address not read at new root")
				}
			}
			if err := validatePoolInternals(pool); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreloadSampledCorrelation(t *testing.T) {
	keys := preloadKeys(1)
	root := common.HexToHash("06")
	pool, _ := newPreloadTestPool(t, map[common.Hash]state.Reader{{}: preloadAccounts(keys, 0, params.Ether), root: preloadAccounts(keys, 1, params.Ether)}, true)
	defer pool.Close()
	if err := pool.addRemoteSync(transaction(0, 21000, keys[0])); err != nil {
		t.Fatal(err)
	}
	pool.mu.Lock()
	pool.reheapSequence = 31
	pool.mu.Unlock()
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	old := pool.currentHead.Load()
	head := preloadHead(old, root)
	pool.Reset(old, head)
	var sample, hold pendingtrace.Event
	for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
		if e.Scope == "preload" {
			sample = e
		}
		if e.Stage == "pool_lock_hold" && e.Source == "runReorg" {
			hold = e
		}
	}
	if sample.Preload == nil || sample.Outcome != "adopted" || sample.Parent != head.Hash() || sample.Pool != hold.Pool || sample.Operation != hold.Operation {
		t.Fatalf("bad correlation: %+v %+v", sample, hold)
	}
	s := sample.Preload
	if s.Root != root || s.Accounts != 1 || s.Loaded != 1 || s.TailLoads != 0 || s.RunToInstallUS > s.RunToUnlockUS || s.RunToUnlockUS > s.RunToCompleteUS {
		t.Fatalf("bad aggregate: %+v", s)
	}
}

func TestPreloadStateFailures(t *testing.T) {
	for _, kind := range []string{"open", "warm_read_once", "warm_read_persistent"} {
		t.Run(kind, func(t *testing.T) {
			keys := preloadKeys(1)
			root := common.HexToHash("07")
			readers := map[common.Hash]state.Reader{{}: preloadAccounts(keys, 0, params.Ether)}
			if kind != "open" {
				readers[root] = preloadAccounts(keys, 1, params.Ether)
			}
			pool, chain := newPreloadTestPool(t, readers, true)
			defer pool.Close()
			if err := pool.addRemoteSync(transaction(0, 21000, keys[0])); err != nil {
				t.Fatal(err)
			}
			if kind != "open" {
				bad := &preloadCountReader{Reader: readers[root], calls: make(map[common.Address]int), err: errors.New("IO failed")}
				chain.prepare = func(root common.Hash) (*state.StateDB, error) { return state.NewWithReader(root, chain.db, bad) }
				if kind == "warm_read_persistent" {
					chain.readers[root] = bad
				}
			}
			old := pool.currentHead.Load()
			oldState := pool.currentState
			head := preloadHead(old, root)
			before := chain.opened.Load()
			pool.mu.Lock()
			pool.reheapSequence = 31
			pool.mu.Unlock()
			cursor := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
			pool.Reset(old, head)
			if chain.prepared.Load() != 1 || chain.opened.Load() <= before {
				t.Fatal("failure skipped original fallback")
			}
			seen := false
			for _, e := range pendingtrace.Snapshot(cursor, pendingtrace.MaxPage).Events {
				if e.Preload != nil {
					seen = true
				}
				if e.Preload != nil && kind != "open" && (e.Outcome != "state_read_error" || e.Preload.ReadErrors != 1 || e.Preload.Loaded != 1) {
					t.Fatalf("partial failure erased: %+v %+v", e, e.Preload)
				}
			}
			if !seen {
				t.Fatal("failure aggregate absent")
			}
			switch kind {
			case "open":
				if pool.currentHead.Load() != old || pool.currentState != oldState {
					t.Fatal("failed open changed head")
				}
			case "warm_read_once":
				if pool.currentHead.Load() != head || pool.currentState.Error() != nil {
					t.Fatal("failed warm state was adopted")
				}
			case "warm_read_persistent":
				if pool.currentState.Error() == nil {
					t.Fatal("original persistent read failure masked")
				}
			}
		})
	}
}

func TestPreloadSetCodeAndSuccessiveRoots(t *testing.T) {
	keys := preloadKeys(2)
	root1 := common.HexToHash("08")
	root2 := common.HexToHash("09")
	var views [][]string
	for _, enabled := range []bool{false, true} {
		pool, chain := newPreloadTestPool(t, map[common.Hash]state.Reader{{}: preloadAccounts(keys, 0, params.Ether), root1: preloadAccounts(keys, 1, params.Ether), root2: preloadAccounts(keys, 0, params.Ether)}, enabled)
		if err := pool.addRemoteSync(setCodeTx(0, keys[0], []unsignedAuth{{0, keys[1]}})); err != nil {
			t.Fatal(err)
		}
		old := pool.currentHead.Load()
		head1 := preloadHead(old, root1)
		pool.Reset(old, head1)
		head2 := preloadHead(head1, root2)
		pool.Reset(head1, head2)
		// Nonce rollback must be read from root2, including authorization accounts.
		if err := pool.addRemoteSync(setCodeTx(0, keys[0], []unsignedAuth{{0, keys[1]}})); err != nil {
			t.Fatal(err)
		}
		if enabled && chain.prepared.Load() != 2 {
			t.Fatal("successive heads did not preload")
		}
		if err := validatePoolInternals(pool); err != nil {
			t.Fatal(err)
		}
		views = append(views, preloadPoolView(pool))
		pool.Close()
	}
	if !reflect.DeepEqual(views[0], views[1]) {
		t.Fatal("SetCode/root rollback differed")
	}
}
