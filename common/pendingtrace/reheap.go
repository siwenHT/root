package pendingtrace

// ReheapStage contains exact invocation counts for one sampled heap stage.
// Type buckets are 0..4 (transaction type) and 5 (other); pairs are ordered.
type ReheapStage struct {
	DurationUS        int64        `json:"duration_us"`
	Comparisons       uint64       `json:"comparisons"`
	NilBaseFee        uint64       `json:"nil_base_fee"`
	ZeroFast          uint64       `json:"zero_fast"`
	ZeroFallback      uint64       `json:"zero_guard_fallback"`
	Nonzero           uint64       `json:"nonzero_base_fee"`
	EffectiveTipEqual uint64       `json:"effective_tip_equal"`
	FeeCapEqual       uint64       `json:"fee_cap_equal"`
	TipCapEqual       uint64       `json:"tip_cap_equal"`
	NonceCompared     uint64       `json:"nonce_compared"`
	NonceEqual        uint64       `json:"nonce_equal"`
	TypePairs         [6][6]uint64 `json:"type_pairs"`
}

// ReheapStats is immutable after publication. Fixed arrays bound record size.
// Times are wall durations, not CPU time. Stages: urgent init, urgent pops,
// floating init. Only complete sampled head-reset rebuilds carry this payload.
type ReheapStats struct {
	Version         int            `json:"version"`
	Rebuild         uint64         `json:"rebuild"`
	SampleEvery     int            `json:"sample_every"`
	Head            uint64         `json:"head"`
	Total           int            `json:"transactions"`
	Pending         int            `json:"pending"`
	Queued          int            `json:"queued"`
	PendingAccounts int            `json:"pending_accounts"`
	QueuedAccounts  int            `json:"queued_accounts"`
	TypeCounts      [6]uint64      `json:"type_counts"`
	StalesBefore    int64          `json:"stales_before"`
	UrgentBefore    int            `json:"urgent_before"`
	FloatingBefore  int            `json:"floating_before"`
	UrgentAfter     int            `json:"urgent_after"`
	FloatingAfter   int            `json:"floating_after"`
	MutexWaitUS     int64          `json:"mutex_wait_us"`
	LookupUS        int64          `json:"lookup_us"`
	TotalUS         int64          `json:"total_us"`
	Stages          [3]ReheapStage `json:"stages"`
}
