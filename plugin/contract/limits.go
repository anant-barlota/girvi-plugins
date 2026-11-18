package contract

import "time"

// The limits Girvi applies to a plugin. A plugin that exceeds one ends that
// call only. Girvi and the test harness both read them from here.
const (
	MemoryLimitMB   = 64
	CallDeadline    = 30 * time.Second
	MaxHTTPResponse = 1 << 20
	MaxHTTPRequest  = 1 << 20 // body
	MaxHTTPRequests = 20      // per call
	MaxStorageKey   = 256
	MaxStorageValue = 1 << 20
	MaxWakePayload  = 64 << 10
	MaxLogLines     = 50
	MaxLogLine      = 500
	MaxStderr       = 16 << 10 // of a crashing plugin's last words

	// What one plugin may keep for one firm.
	MaxStorageKeys  = 1000
	MaxStorageBytes = 16 << 20
	MaxWakeUps      = 100 // scheduled and not yet had

	// MinWakeDelay is the soonest a wake-up can be, so a plugin cannot keep
	// itself running in a tight loop. MaxDelay is the latest a wake-up or a
	// retry can be.
	MinWakeDelay = time.Second
	MaxDelay     = 365 * 24 * time.Hour
)

// HostModule is the import module of Girvi's host functions.
const HostModule = "girvi_v1"

// Results of the storage and wake-up host functions.
const (
	HostOK      int32 = 0
	HostMissing int32 = -1
	HostError   int32 = -2
)
