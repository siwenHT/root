package eth

import (
	"fmt"
	"os"
	"strconv"
)

// Snapshot window knobs for the deep pool reads served by arb_getPostPoolState.
// They are read once at process start from the environment (the same pattern
// as ARB_V3_CACHE_MODE) and are process-global, so the per-parent-root V3 cache
// never mixes snapshots taken with different windows. Unset or invalid values
// fall back to the defaults, which reproduce the pre-patch behaviour exactly:
// V3 reads base?2 bitmap words and the nearest 16 initialized ticks per side;
// Infinity CL / V4 singletons read base?1 words and every initialized tick in
// them.
//
// Words beyond the legacy radius ("extended words") are only published when the
// node has read every initialized tick inside them (and in every word between
// them and the base word). The published bitmap/coverage therefore stays
// honest: a client either sees a complete word or no word at all, and keeps
// raising CacheMiss beyond the published coverage exactly as before.
const (
	envV3WindowRadius           = "ARB_V3_WINDOW_RADIUS"
	envV3TicksPerSide           = "ARB_V3_TICKS_PER_SIDE"
	envSingletonWindowRadius    = "ARB_SINGLETON_WINDOW_RADIUS"
	envSingletonExtTicksPerSide = "ARB_SINGLETON_EXT_TICKS_PER_SIDE"

	legacyV3WindowRadius        = 2
	legacyV3TicksPerSide        = 16
	legacySingletonWindowRadius = 1

	maxV3WindowRadius        = 8
	maxV3TicksPerSide        = 256
	maxSingletonWindowRadius = 6
	// Default cap on initialized ticks read from extended singleton words per
	// side. Legacy words are never capped (unchanged behaviour); 0 = unlimited.
	defaultSingletonExtTicksPerSide = 128
	maxSingletonExtTicksPerSide     = 2048
)

type arbWindowConfig struct {
	V3Radius              int64
	V3TicksPerSide        int
	SingletonRadius       int64
	SingletonExtTicksSide int // 0 = unlimited
	Invalid               []string
}

func defaultArbWindowConfig() arbWindowConfig {
	return arbWindowConfig{
		V3Radius:              legacyV3WindowRadius,
		V3TicksPerSide:        legacyV3TicksPerSide,
		SingletonRadius:       legacySingletonWindowRadius,
		SingletonExtTicksSide: defaultSingletonExtTicksPerSide,
	}
}

// IsLegacy reports whether the config reads exactly what the unpatched node read.
func (c arbWindowConfig) IsLegacy() bool {
	return c.V3Radius == legacyV3WindowRadius && c.V3TicksPerSide == legacyV3TicksPerSide &&
		c.SingletonRadius == legacySingletonWindowRadius
}

func (c arbWindowConfig) String() string {
	return fmt.Sprintf("v3_radius=%d v3_ticks_per_side=%d singleton_radius=%d singleton_ext_ticks_per_side=%d legacy=%v",
		c.V3Radius, c.V3TicksPerSide, c.SingletonRadius, c.SingletonExtTicksSide, c.IsLegacy())
}

func parseWindowInt(getenv func(string) string, key string, def, lo, hi int64, invalid *[]string) int64 {
	raw := getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < lo || v > hi {
		*invalid = append(*invalid, key+"="+raw)
		return def
	}
	return v
}

func loadArbWindowConfig(getenv func(string) string) arbWindowConfig {
	c := defaultArbWindowConfig()
	c.V3Radius = parseWindowInt(getenv, envV3WindowRadius, legacyV3WindowRadius, 1, maxV3WindowRadius, &c.Invalid)
	c.V3TicksPerSide = int(parseWindowInt(getenv, envV3TicksPerSide, legacyV3TicksPerSide, legacyV3TicksPerSide, maxV3TicksPerSide, &c.Invalid))
	c.SingletonRadius = parseWindowInt(getenv, envSingletonWindowRadius, legacySingletonWindowRadius, 1, maxSingletonWindowRadius, &c.Invalid)
	c.SingletonExtTicksSide = int(parseWindowInt(getenv, envSingletonExtTicksPerSide, defaultSingletonExtTicksPerSide, 0, maxSingletonExtTicksPerSide, &c.Invalid))
	return c
}

// arbWindowSetting is the process-wide window. Tests swap it via withArbWindow.
var arbWindowSetting = loadArbWindowConfig(os.Getenv)
