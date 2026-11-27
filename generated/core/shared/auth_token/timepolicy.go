package auth_token

import (
	"errors"
	"fmt"
	"time"

	"milton_prism/core/shared/cache_client"
	"milton_prism/pkg/config"
)

// MaxLeeway is the largest clock skew a validator accepts between the issuer's clock
// and its own. A service that needs more has a clock problem, not a token problem.
const MaxLeeway = 2 * time.Minute

// TimePolicy is how a validator judges exp, nbf and iat.
//
//   - Now is the clock; nil means time.Now. Tests inject a fixed one.
//   - Leeway is the skew tolerated in both directions: a token stays valid until
//     exp+Leeway, and is accepted from nbf-Leeway and iat-Leeway. The zero value is no
//     leeway (the platform's own services); the generated-service runtime sets 30s
//     (svcruntime.DefaultTokenLeeway). It must be between 0 and MaxLeeway.
//
// exp is always required, and exp == now (plus leeway) is expired.
type TimePolicy struct {
	Now    func() time.Time
	Leeway time.Duration
}

var (
	errTokenExpired     = errors.New("token expired")
	errTokenNoExpiry    = errors.New("token has no expiry")
	errTokenNotYetValid = errors.New("token not yet valid")
	errTokenFutureIAT   = errors.New("token issued in the future")
)

// validate checks the leeway bounds.
func (p TimePolicy) validate() error {
	if p.Leeway < 0 || p.Leeway > MaxLeeway {
		return fmt.Errorf("token clock leeway must be between 0 and %s", MaxLeeway)
	}
	return nil
}

func (p TimePolicy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// check applies the policy to the (possibly absent) time claims.
func (p TimePolicy) check(exp, nbf, iat *time.Time) error {
	now := p.now()
	if exp == nil || exp.IsZero() {
		return errTokenNoExpiry
	}
	if !now.Before(exp.Add(p.Leeway)) {
		return errTokenExpired
	}
	if nbf != nil && !nbf.IsZero() && now.Add(p.Leeway).Before(*nbf) {
		return errTokenNotYetValid
	}
	if iat != nil && !iat.IsZero() && now.Add(p.Leeway).Before(*iat) {
		return errTokenFutureIAT
	}
	return nil
}

// NewTokenValidatorWithPolicy is NewTokenValidator with an explicit time policy.
func NewTokenValidatorWithPolicy(cfg *config.TokenValidatorConfig, cache *cache_client.TokenBlacklistCache, p TimePolicy) (TokenValidator, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	v, err := NewTokenValidator(cfg, cache)
	if err != nil {
		return nil, err
	}
	switch t := v.(type) {
	case *JWTValidator:
		t.policy = p
	case *PasetoValidator:
		t.policy = p
	}
	return v, nil
}
