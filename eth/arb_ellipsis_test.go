package eth

import (
	"encoding/json"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/arb"
)

func TestEllipsisReaderAgainstRealBSCContracts(t *testing.T) {
	raw, err := os.ReadFile("testdata/ellipsis-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Timestamp  uint64 `json:"timestamp"`
		Block      uint64 `json:"block"`
		Allocation map[common.Address]struct {
			Code    hexutil.Bytes               `json:"code"`
			Storage map[common.Hash]common.Hash `json:"storage"`
		} `json:"allocation"`
		VirtualPrices map[common.Address]string `json:"virtual_prices"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	x, _, chain := prefixFixture(t)
	defer chain.Stop()
	base := x.base.Copy()
	for address, account := range fixture.Allocation {
		base.SetCode(address, account.Code, 0)
		for slot, value := range account.Storage {
			base.SetState(address, slot, value)
		}
	}
	header := types.CopyHeader(x.parent)
	header.Time = fixture.Timestamp
	header.Number = new(big.Int).SetUint64(fixture.Block)
	for pool, expected := range fixture.VirtualPrices {
		caller := newPoolCallerOver(chain, base, header, newReadBudget(defaultMaxReads, 5*time.Second))
		got, err := readEllipsisState(caller, pool)
		if err != nil {
			t.Fatalf("%s: %v", pool, err)
		}
		if got.Stable["get_virtual_price()"] != expected {
			t.Fatalf("%s got %v expected %s", pool, got.Stable["get_virtual_price()"], expected)
		}
		if ellipsisBase(pool) != (common.Address{}) && got.Base == nil {
			t.Fatal("missing base dependency")
		}
		if pool == ellipsisValBase {
			available := got.Stable["available"].([]string)
			if new(big.Int).SetBytes(common.FromHex(available[1])).Sign() != 0 || new(big.Int).SetBytes(common.FromHex(available[2])).Sign() != 0 {
				t.Fatal("fixture withdrawal cash changed")
			}
			enabled := got.Stable["deposit_enabled"].([]bool)
			if enabled[0] || !enabled[1] || !enabled[2] {
				t.Fatal("reserve configuration not preserved")
			}
		}
	}

	svc, err := (&Ethereum{blockchain: chain}).newArbService(arbServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity := arb.HandleIdentity{OwnerSession: arb.RandomID32(), NodeBootID: svc.boot}
	identity.Identity = identity.OwnerSession
	parent, _, err := svc.pinParent(identity, x.parent.Hash(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.releaseParent(parent)
	child := svc.registerPostChild(parent, identity, base, x.parent)
	defer svc.releaseParent(child)
	env := validEnvForExecutor(x)
	env.TimestampSeconds = strconv.FormatUint(fixture.Timestamp, 10)
	env.TimestampMs = strconv.FormatUint(fixture.Timestamp*1000, 10)
	env, err = x.prepareBlockEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	args := GetEllipsisStateArgs{ParentHash: x.parent.Hash().Hex(), RequestID: identity.Identity, PostHandle: child, BlockEnv: env, BudgetMicros: 5_000_000, Pool: strings.ToLower(ellipsisValBase.Hex())}
	result, err := (&ArbAPI{svc: svc}).GetEllipsisState(args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Timestamp != fixture.Timestamp || result.Boot != svc.boot || result.Stable["get_virtual_price()"] != fixture.VirtualPrices[ellipsisValBase] {
		t.Fatal("wrong post-handle state or child timestamp")
	}
	args.ParentHash = common.Hash{}.Hex()
	if _, err := (&ArbAPI{svc: svc}).GetEllipsisState(args); err == nil {
		t.Fatal("accepted foreign parent")
	}
	if len(x.base.GetCode(ellipsisOldBase)) != 0 {
		t.Fatal("read fixture leaked into canonical parent")
	}
}

func TestEllipsisRejectsUnrecognizedPoolBeforeReading(t *testing.T) {
	_, err := (&ArbAPI{}).GetEllipsisState(GetEllipsisStateArgs{})
	if err == nil {
		t.Fatal("accepted empty state request")
	}
}
