package config

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
)

// installationUUIDPattern only accepts RFC 4122 random v4 UUIDs. This identifier
// is not a device fingerprint and grants no authentication or authorization.
var installationUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// EnsureInstanceID creates one per installed client configuration, persists it
// before it can be sent to the server, and never rotates it during re-pairing.
// An invalid existing value is an error, not an invitation to silently rotate
// the installation identity.
func EnsureInstanceID(c *Config) error {
	if c == nil {
		return fmt.Errorf("client configuration missing")
	}
	if c.InstanceID != "" {
		if !installationUUIDPattern.MatchString(c.InstanceID) {
			return fmt.Errorf("invalid persisted instance_id")
		}
		c.InstanceID = strings.ToLower(c.InstanceID)
		return nil
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate instance_id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // UUID version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant
	next := fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	c.InstanceID = next
	if err := Persist(*c); err != nil {
		c.InstanceID = ""
		return fmt.Errorf("persist instance_id: %w", err)
	}
	return nil
}
