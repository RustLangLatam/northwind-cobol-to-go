package svcruntime

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // the zone database is linked in: distroless images carry no zoneinfo

	applog "milton_prism/pkg/log"
)

// Environment variables of the business clock.
const (
	EnvServiceTimeZone = "SERVICE_TIME_ZONE" // IANA zone of the source system, e.g. Europe/Madrid
	EnvTZ              = "TZ"                // used when SERVICE_TIME_ZONE is unset
)

// Clock is the business clock: the time of the source system's local zone. A COBOL
// ACCEPT FROM DATE/TIME or CURRENT-DATE reads this clock, never time.Now() in UTC.
type Clock struct {
	loc *time.Location
	now func() time.Time
}

// ClockFromEnv reads the zone from SERVICE_TIME_ZONE, else TZ. Neither set is an
// error: the zone is a fact of the source system and must be declared.
func ClockFromEnv(getenv func(string) string) (Clock, error) {
	source := EnvServiceTimeZone
	name := strings.TrimSpace(getenv(EnvServiceTimeZone))
	if name == "" {
		source = EnvTZ
		name = strings.TrimPrefix(strings.TrimSpace(getenv(EnvTZ)), ":")
	}
	if name == "" {
		return Clock{}, fmt.Errorf("svcruntime: %s (or %s) must name the source system's time zone, e.g. Europe/Madrid", EnvServiceTimeZone, EnvTZ)
	}
	if strings.EqualFold(name, "Local") {
		return Clock{}, fmt.Errorf("svcruntime: time zone %q is the host's zone, which a container does not declare; name an IANA zone such as Europe/Madrid", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Clock{}, fmt.Errorf("svcruntime: time zone %q is not a known IANA zone (%s): %w", clip(name), source, err)
	}
	applog.Infof("svcruntime: business clock zone=%s (from %s)", loc, source)
	return NewClock(loc, nil), nil
}

// clip keeps a zone name from an environment variable short in an error message.
func clip(s string) string {
	if len(s) > 64 {
		return s[:64] + "..."
	}
	return s
}

// NewClock returns a clock in loc (nil panics). now nil is time.Now; tests pass a
// fixed function.
func NewClock(loc *time.Location, now func() time.Time) Clock {
	if loc == nil {
		panic("svcruntime: NewClock needs a location")
	}
	if now == nil {
		now = time.Now
	}
	return Clock{loc: loc, now: now}
}

// Now is the current time in the clock's zone. A zero Clock panics: a clock without
// a declared zone would silently answer in UTC.
func (c Clock) Now() time.Time {
	loc := c.Location()
	return c.now().In(loc)
}

// Location is the clock's zone. A zero Clock panics.
func (c Clock) Location() *time.Location {
	if c.loc == nil || c.now == nil {
		panic("svcruntime: zero Clock; build it with ClockFromEnv or NewClock")
	}
	return c.loc
}
