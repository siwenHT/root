package pendingtrace

import "github.com/ethereum/go-ethereum/common"

// PreloadStats is one immutable aggregate per 32 head-reset rebuilds. Reader
// loads are object-cache misses/attempts, not physical disk reads.
type PreloadStats struct {
	Version         int         `json:"version"`
	Rebuild         uint64      `json:"rebuild"`
	SampleEvery     int         `json:"sample_every"`
	Root            common.Hash `json:"root"`
	SnapshotWaitUS  int64       `json:"snapshot_wait_us"`
	SnapshotHoldUS  int64       `json:"snapshot_hold_us"`
	StateOpenUS     int64       `json:"state_open_us"`
	NoncerUS        int64       `json:"noncer_us"`
	ReadUS          int64       `json:"read_us"`
	ReadErrors      int         `json:"read_errors"`
	Accounts        int         `json:"accounts"`
	Loaded          int         `json:"loaded"`
	AccountReadsUS  int64       `json:"account_reads_us"`
	TailLoads       int         `json:"tail_loads"`
	RunToInstallUS  int64       `json:"run_to_install_us"`
	RunToUnlockUS   int64       `json:"run_to_unlock_us"`
	RunToCompleteUS int64       `json:"run_to_complete_us"`
	WriteWaitUS     int64       `json:"write_wait_us"`
	WriteHoldUS     int64       `json:"write_hold_us"`
}
