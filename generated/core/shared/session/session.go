// Package session defines the Session type and validity rules used across
// the authentication and authorization flows.
package session

import (
	"time"

	"milton_prism/core/shared/tenant"
)

// Session represents a user's login session stored in cache.
type Session struct {
	SessionID  string    `json:"session_id"`   // UUID for the session
	UserID     uint64    `json:"user_id"`      // User's unique identifier (typically from token claims)
	TenantID   uint64    `json:"tenant_id"`    // Tenant the session belongs to (0 = default tenant, ESC-6 #40)
	SystemUser bool      `json:"system_user"`  // User is a system user
	RefreshJTI string    `json:"refresh_jti"`  // JWT Identifier of the refresh token
	AccessJTI  string    `json:"access_jti"`   // JWT Identifier of the access token
	ExpiresAt  time.Time `json:"expires_at"`   // Refresh token expiration timestamp
	CreatedAt  time.Time `json:"created_at"`   // When the session was created
	LastUsedAt time.Time `json:"last_used_at"` // When the session was last active
	DeviceInfo string    `json:"device_info"`  // Device fingerprint / user-agent
	IPAddress  string    `json:"ip_address"`   // Client IP address
	IsRevoked  bool      `json:"is_revoked"`   // True if session was manually invalidated
}

// Tenant returns the tenant the session belongs to. It lets a *Session satisfy
// the tenant.Derive input interface. A session created before multi-tenancy has
// TenantID == 0, which is the default tenant — so existing sessions keep working.
func (s *Session) Tenant() tenant.ID {
	return tenant.ID(s.TenantID)
}

// IsExpired returns true if the session has expired based on refresh token expiry.
func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// IsValid returns true if the session is still active and not revoked.
func (s *Session) IsValid() bool {
	return !s.IsRevoked && !s.IsExpired()
}

// AccessJTIAllowed reports whether a presented access-token JTI is allowed for
// this session — the canonical token<->session binding check (#34) used by the
// core gRPC verify_token_ctx path and the SSE endpoint alike.
//
// An empty stored AccessJTI means the session predates token<->session binding
// (a legacy/unbound session — including seeded system-token sessions that don't
// set it); those are accepted so enabling the check never locks out live,
// pre-existing sessions. Once a session carries a bound JTI, the presented token
// MUST match it exactly — so a rotated (refreshed-away) access token is rejected.
func (s *Session) AccessJTIAllowed(presentedJTI string) bool {
	if s.AccessJTI == "" {
		return true // not yet bound → accept (no lockout for legacy/system sessions)
	}
	return s.AccessJTI == presentedJTI
}

// Revoke marks the session as invalid.
func (s *Session) Revoke() {
	s.IsRevoked = true
}

// MarkUsed updates the last-used timestamp.
func (s *Session) MarkUsed() {
	s.LastUsedAt = time.Now()
}

// IsSessionValid performs integrity checks for a session
func (s *Session) IsSessionValid() bool {
	if s.IsRevoked {
		return false
	}
	if time.Now().After(s.ExpiresAt) {
		return false
	}
	return true
}
