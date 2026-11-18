package eth

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var squadBinManager = common.HexToAddress("0x7f8e5a02f9007b19408ef731982adbba7ae08eb1")
var squadBinVault = common.HexToAddress("0xb1ba60cdb44c88ec5366ab94dcb2f992b31f71f2")
var squadBinHook = common.HexToAddress("0xe7c583425bb682ac321701aa954e99a352c5aa53")
var squadBinPool = common.HexToHash("0xef6690bc847cc65e312483156a5ac385eae75ce7f9299155d0a502f42264c7d7")

func squadBinSelector(signature string) []byte { return crypto.Keccak256([]byte(signature))[:4] }

var (
	squadSlot0 = squadBinSelector("getSlot0(bytes32)")
	squadNext  = squadBinSelector("getNextNonEmptyBin(bytes32,bool,uint24)")
	squadBin   = squadBinSelector("getBin(bytes32,uint24)")
	squadFee   = squadBinSelector("swapFees(bytes32)")
	squadPause = squadBinSelector("paused()")
)

var errSquadBinUnavailable = errors.New("arb: SquadSwap Bin deployment, pause or hook fee unsupported")

func readSquadBinState(c *poolCaller) ([]byte, error) {
	if c.wrapped.GetCodeHash(squadBinManager) != common.HexToHash("0xe446646024ae26c50b94ef173694b42071e50759396a0c85fca4d00f4f712349") ||
		c.wrapped.GetCodeHash(squadBinHook) != common.HexToHash("0x920fed387d79bf40e86e7ed8f012f4e4480afc3080e7b671b453c10faceeab2a") ||
		c.wrapped.GetCodeHash(squadBinVault) != common.HexToHash("0x7959d347ce64362df7d8b3329a73848b5c8d132ff51cef99695b39068db66473") {
		return nil, errSquadBinUnavailable
	}
	paused, err := c.staticCall(squadBinManager, squadPause)
	if err != nil {
		return nil, err
	}
	fee, err := c.staticCall(squadBinHook, append(squadFee, squadBinPool[:]...))
	if err != nil {
		return nil, err
	}
	var zero common.Hash
	if !bytes.Equal(paused, zero[:]) || !bytes.Equal(fee, zero[:]) {
		return nil, errSquadBinUnavailable
	}
	slot, err := c.staticCall(squadBinManager, append(squadSlot0, squadBinPool[:]...))
	if err != nil {
		return nil, err
	}
	if len(slot) != 96 {
		return nil, errSquadBinUnavailable
	}
	active := binary.BigEndian.Uint32(slot[28:32])
	data := append([]byte{}, slot...)
	var binCall [68]byte
	copy(binCall[:4], squadBin)
	copy(binCall[4:36], squadBinPool[:])
	appendBin := func(id uint32) error {
		binary.BigEndian.PutUint32(binCall[64:], id)
		bin, err := c.staticCall(squadBinManager, binCall[:])
		if err != nil {
			return err
		}
		if len(bin) != 128 {
			return errSquadBinUnavailable
		}
		data = append(data, binCall[36:]...)
		data = append(data, bin[:64]...)
		return nil
	}
	if err := appendBin(active); err != nil {
		return nil, err
	}
	var nextCall [100]byte
	copy(nextCall[:4], squadNext)
	copy(nextCall[4:36], squadBinPool[:])
	for _, direction := range []byte{1, 0} {
		nextCall[67] = direction
		id := active
		for {
			binary.BigEndian.PutUint32(nextCall[96:], id)
			next, err := c.staticCall(squadBinManager, nextCall[:])
			if err != nil {
				return nil, err
			}
			if len(next) != 32 {
				return nil, errSquadBinUnavailable
			}
			id = binary.BigEndian.Uint32(next[28:])
			if id == 0 || id == 0xffffff {
				break
			}
			if err := appendBin(id); err != nil {
				return nil, err
			}
		}
	}
	return data, nil
}

func (api *ArbAPI) GetSquadBinState(args GetFluxPoolStateArgs) (*GetFluxPoolStateResult, error) {
	return api.readPreparedPoolState(args, readSquadBinState)
}
