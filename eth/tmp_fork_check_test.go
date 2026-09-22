package eth

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

func tmpRun(t *testing.T, config *params.ChainConfig, initcode string) (uint64, uint64) {
	key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	addr := crypto.PubkeyToAddress(key.PublicKey)
	genesis := &core.Genesis{Config: config, GasLimit: 30000000, BaseFee: new(big.Int), Alloc: types.GenesisAlloc{addr: {Balance: big.NewInt(1e18)}}}
	gdb := rawdb.NewMemoryDatabase()
	gb := genesis.MustCommit(gdb, triedb.NewDatabase(gdb, triedb.HashDefaults))
	signer := types.LatestSigner(genesis.Config)
	tx, _ := types.SignTx(types.NewContractCreation(0, big.NewInt(0), 1000000, big.NewInt(0), common.FromHex(initcode)), signer, key)
	_, receipts := core.GenerateChain(genesis.Config, gb, ethash.NewFaker(), gdb, 1, func(i int, g *core.BlockGen) { g.AddTx(tx) })
	return receipts[0][0].Status, receipts[0][0].GasUsed
}

func TestTmpForkRules(t *testing.T) {
	merged := *params.MergedTestChainConfig
	merged.PragueTime = nil
	merged.OsakaTime = nil
	n := big.NewInt(1)
	for _, c := range []*params.ChainConfig{&merged, params.TestChainConfig} {
		for _, isMerge := range []bool{true, false} {
			r := c.Rules(n, isMerge, 10)
			t.Logf("DEBUG rules chainID=%v isMerge=%v shanghai=%v cancun=%v prague=%v", c.ChainID, isMerge, r.IsShanghai, r.IsCancun, r.IsPrague)
		}
	}
}

func TestTmpForkFeatures(t *testing.T) {
	merged := *params.MergedTestChainConfig
	merged.PragueTime = nil
	merged.OsakaTime = nil
	// PUSH0 POP STOP ; MCOPY(x,x,x) POP STOP ; TSTORE(0,0)
	cases := map[string]string{"push0": "0x5f5000", "mcopy": "0x5f5f5f5e5000", "tstore": "0x5f5f5d00", "plain": "0x00"}
	for name, config := range map[string]*params.ChainConfig{"merged_cancun": &merged, "test": params.TestChainConfig} {
		for cname, code := range cases {
			status, gas := tmpRun(t, config, code)
			t.Logf("DEBUG fork=%s feature=%s status=%d gas=%d", name, cname, status, gas)
		}
	}
}
