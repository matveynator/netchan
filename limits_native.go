//go:build !baremetal && !netchan_small

package netchan

const (
	maximumPendingFrames        = 1024
	channelFrameBufferSize      = maximumPendingFrames + 32
	maximumValueCapabilities    = 16
	maximumSessionCapabilities  = 256
	maximumSessionQueuedBytes   = 64 << 20
	maximumSessionPreparedBytes = 64 << 20
	maximumSessionDecodedBytes  = 64 << 20
	maximumCapabilityBytes      = 64 << 20
	maximumCapabilityRouteBytes = 64 << 20
	maximumNodeSessions         = 1024
	maximumConcurrentHandshakes = 64
	maximumConcurrentEncodings  = 4
	maximumWireFrameSize        = 16 << 20
	maximumChannelPayloadSize   = maximumWireFrameSize - 1024
	maximumPayloadDepth         = 64
	maximumCollectionLength     = 1 << 20
	maximumDecodeAllocation     = 64 << 20
)
