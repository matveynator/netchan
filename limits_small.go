//go:build baremetal || netchan_small

package netchan

// Embedded budgets reject oversized work before allocation without changing
// the v2 encoding. A Go peer must send values within the target limits.
const (
	maximumPendingFrames        = 8
	channelFrameBufferSize      = maximumPendingFrames + 4
	maximumValueCapabilities    = 4
	maximumSessionCapabilities  = 8
	maximumSessionQueuedBytes   = 8 << 10
	maximumSessionPreparedBytes = 8 << 10
	maximumSessionDecodedBytes  = 8 << 10
	maximumCapabilityBytes      = 8 << 10
	maximumCapabilityRouteBytes = 8 << 10
	maximumNodeSessions         = 2
	maximumConcurrentHandshakes = 2
	maximumConcurrentEncodings  = 1
	maximumWireFrameSize        = 4 << 10
	maximumChannelPayloadSize   = maximumWireFrameSize - 1024
	maximumPayloadDepth         = 16
	maximumCollectionLength     = 4096
	maximumDecodeAllocation     = 8 << 10
)
