package config

import (
	"fmt"
	"math"
)

// ServerOptionCgf represents the configuration for gRPC services.
type ServerOptionCgf struct {
	// Maximum receive message size for service (in MB)
	MaxRecvMsgSizeMB int `toml:"maxRecvMsgSizeMb"`
	// Maximum send message size for service (in MB)
	MaxSendMsgSizeMB int `toml:"maxSendMsgSizeMb"`
}

// Default values for SettingsConfig
const (
	// MaxAllowedMsgSizeBytes defines the upper limit for message sizes (2 GiB, based on MaxInt32)
	MaxAllowedMsgSizeBytes = math.MaxInt32 // 2,147,483,647 bytes (~2048 MB)

	// MaxGRPCMessageSizeBytes is the shared "comfortably large" gRPC message-size cap
	// (1 GiB) applied on both send and receive, on every gRPC server and every
	// gRPC client/dial. It gives large migration responses (well over the ~122 MiB
	// that overflowed gRPC's prior 100 MiB default) ample headroom.
	MaxGRPCMessageSizeBytes = 1073741824 // 1 GiB

	// DefaultMaxRecvMsgSizeBytes sets the default maximum receive message size to 1 GiB
	DefaultMaxRecvMsgSizeBytes = MaxGRPCMessageSizeBytes

	// DefaultMaxSendMsgSizeBytes sets the default maximum send message size to 1 GiB
	// This value must be less than or equal to MaxAllowedMsgSizeBytes
	DefaultMaxSendMsgSizeBytes = MaxGRPCMessageSizeBytes
)

// Validate checks the ServerOptionCgf fields for valid values and sets defaults if necessary.
func (c *ServerOptionCgf) Validate() error {
	// Set default for MaxRecvMsgSizeMB if it's not specified or invalid
	if c.MaxRecvMsgSizeMB <= 0 {
		c.MaxRecvMsgSizeMB = DefaultMaxRecvMsgSizeBytes
	} else if c.MaxRecvMsgSizeMB > MaxAllowedMsgSizeBytes {
		return fmt.Errorf("MaxRecvMsgSizeMB (%d MB) exceeds allowed limit of %d MB", c.MaxRecvMsgSizeMB, MaxAllowedMsgSizeBytes)
	} else {
		c.MaxRecvMsgSizeMB = c.MaxRecvMsgSizeMB * 1024 * 1024
	}

	// Set default for MaxSendMsgSizeMB if it's not specified or invalid
	if c.MaxSendMsgSizeMB <= 0 {
		c.MaxSendMsgSizeMB = DefaultMaxSendMsgSizeBytes
	} else if c.MaxSendMsgSizeMB > MaxAllowedMsgSizeBytes {
		return fmt.Errorf("MaxSendMsgSizeMB (%d MB) exceeds allowed limit of %d MB", c.MaxSendMsgSizeMB, MaxAllowedMsgSizeBytes)
	} else {
		c.MaxSendMsgSizeMB = c.MaxSendMsgSizeMB * 1024 * 1024
	}

	return nil
}
