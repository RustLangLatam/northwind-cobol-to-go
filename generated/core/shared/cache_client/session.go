package cache_client

import (
	"encoding/json"
	"errors"
	"fmt"
	"milton_prism/core/shared/session"
	paniccontrol "milton_prism/core/shared/utils"
	applog "milton_prism/pkg/log"
	"strconv"
	"time"

	"github.com/gomodule/redigo/redis"
)

type SessionCache struct {
	*Cache
	ttl time.Duration
}

// ErrSessionNotFound means the session data is absent (expired or deleted). Any
// other error from the lookup is an infrastructure fault and must not be read as
// "the session is gone".
var ErrSessionNotFound = errors.New("session not found")

const (
	userSessionKeyPrefix = "user:sessions:" // Set of session IDs per user
	sessionDataKeyPrefix = "session:data:"  // Session data per session Identifier
)

// NewSessionCache returns a new instance of SessionCache
func NewSessionCache(pool *Cache, ttl time.Duration) *SessionCache {
	return &SessionCache{
		Cache: pool,
		ttl:   ttl,
	}
}

// SaveSession stores a new session
func (sc *SessionCache) SaveSession(s *session.Session) error {
	conn := sc.GetConn()
	defer func() {
		if err := conn.Close(); err != nil {
			applog.Warningf("cache: connection close: error=%v", err)
		}
		paniccontrol.RecoverFromPanic()
	}()

	sessionKey := sessionDataKeyPrefix + s.SessionID
	userSetKey := userSessionKeyPrefix + strconv.FormatUint(s.UserID, 10)

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}

	// Store session data with TTL
	if _, err := conn.Do("SETEX", sessionKey, int(sc.ttl.Seconds()), data); err != nil {
		return err
	}

	// Add session Identifier to the user's session set
	if _, err := conn.Do("SADD", userSetKey, s.SessionID); err != nil {
		return err
	}

	// Set TTL on the user session set (optional, can be removed if you want it permanent)
	if _, err := conn.Do("EXPIRE", userSetKey, int(sc.ttl.Seconds())); err != nil {
		return err
	}

	return nil
}

// GetSessionByID retrieves session data by session Identifier
func (sc *SessionCache) GetSessionByID(sessionID string) (*session.Session, error) {
	conn := sc.GetConn()
	defer func() {
		if err := conn.Close(); err != nil {
			applog.Warningf("cache: connection close: error=%v", err)
		}
		paniccontrol.RecoverFromPanic()
	}()

	data, err := redis.Bytes(conn.Do("GET", sessionDataKeyPrefix+sessionID))
	if err != nil {
		if errors.Is(err, redis.ErrNil) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}

	var s session.Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}

	return &s, nil
}

// GetAllUserSessions fetches all session objects for a user
func (sc *SessionCache) GetAllUserSessions(userID uint64) ([]*session.Session, error) {
	conn := sc.GetConn()
	defer func() {
		if err := conn.Close(); err != nil {
			applog.Warningf("cache: connection close: error=%v", err)
		}
		paniccontrol.RecoverFromPanic()
	}()

	userSetKey := userSessionKeyPrefix + strconv.FormatUint(userID, 10)
	sessionIDs, err := redis.Strings(conn.Do("SMEMBERS", userSetKey))
	if err != nil {
		return nil, err
	}

	var sessions []*session.Session
	for _, sid := range sessionIDs {
		s, err := sc.GetSessionByID(sid)
		if errors.Is(err, ErrSessionNotFound) {
			continue // data expired; its index entry lapses with the index TTL
		}
		if err != nil {
			return nil, fmt.Errorf("read session: %w", err)
		}
		sessions = append(sessions, s)
	}

	return sessions, nil
}

// RevokeAllUserSessions deletes every session in the user's index. It needs no
// decoding, so a corrupt blob is removed too, and it fails on any cache error
// instead of reporting success with live sessions. Only the ids it saw are removed
// from the index, so a session saved concurrently stays indexed for the caller's
// second pass.
func (sc *SessionCache) RevokeAllUserSessions(userID uint64) error {
	conn := sc.GetConn()
	defer func() {
		if err := conn.Close(); err != nil {
			applog.Warningf("cache: connection close: error=%v", err)
		}
		paniccontrol.RecoverFromPanic()
	}()

	userSetKey := userSessionKeyPrefix + strconv.FormatUint(userID, 10)
	sessionIDs, err := redis.Strings(conn.Do("SMEMBERS", userSetKey))
	if err != nil {
		return err
	}
	if len(sessionIDs) == 0 {
		return nil
	}
	if err := conn.Send("MULTI"); err != nil {
		return err
	}
	srem := make([]interface{}, 0, len(sessionIDs)+1)
	srem = append(srem, userSetKey)
	for _, sid := range sessionIDs {
		if err := conn.Send("DEL", sessionDataKeyPrefix+sid); err != nil {
			return err
		}
		srem = append(srem, sid)
	}
	if err := conn.Send("SREM", srem...); err != nil {
		return err
	}
	_, err = conn.Do("EXEC")
	return err
}

// RevokeSession marks a session as revoked and updates it
func (sc *SessionCache) RevokeSession(sessionID string) error {
	s, err := sc.GetSessionByID(sessionID)
	if err != nil {
		return err
	}

	s.IsRevoked = true
	s.LastUsedAt = time.Now()
	return sc.SaveSession(s)
}

// DeleteSession completely removes a session
func (sc *SessionCache) DeleteSession(sessionID string, userID string) error {
	conn := sc.GetConn()
	defer func() {
		if err := conn.Close(); err != nil {
			applog.Warningf("cache: connection close: error=%v", err)
		}
		paniccontrol.RecoverFromPanic()
	}()

	_, err := conn.Do("DEL", sessionDataKeyPrefix+sessionID)
	if err != nil {
		return err
	}

	_, err = conn.Do("SREM", userSessionKeyPrefix+userID, sessionID)
	return err
}
