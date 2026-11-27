package svcruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"milton_prism/core/shared/auth_token"
	"milton_prism/pkg/config"
	applog "milton_prism/pkg/log"
)

// healthCheck is the only method callable without credentials (probes). Watch is
// a stream and stays authenticated.
const healthCheck = "/grpc.health.v1.Health/Check"

var (
	errUnauthenticated = status.Error(codes.Unauthenticated, "authentication required")
	errCredsExpired    = status.Error(codes.Unauthenticated, "credentials expired")
	errScope           = status.Error(codes.PermissionDenied, "the credentials lack a scope this method requires")
)

// Caller is the authenticated identity behind a call, available to handlers and
// service interceptors through CallerFromContext. It carries no secret: the token
// itself is never exposed.
type Caller struct {
	Subject string    // token "sub", or the matched certificate name
	TokenID string    // token "jti" (empty for mtls)
	Scopes  []string  // token "scopes" (nil for mtls)
	Expires time.Time // token exp or certificate NotAfter
}

type callerKey struct{}

// CallerFromContext returns the caller the authentication interceptor stored. It is
// absent only for the unauthenticated health Check.
func CallerFromContext(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// authenticator decides whether the caller behind ctx may call the service.
type authenticator func(ctx context.Context) (Caller, error)

// RevocationCheck reports whether an otherwise valid token was revoked. It is the hook
// for a service that has a revocation store (the platform's KeyDB blacklist, a list
// pushed by the identity service). It runs once per call, and once when a stream
// opens; an error counts as revoked (fail closed).
type RevocationCheck func(ctx context.Context, c Caller) (revoked bool, err error)

func newAuthenticator(cfg Config, o options) (authenticator, error) {
	switch cfg.AuthMode {
	case AuthToken:
		return tokenAuthenticator(cfg, o)
	case AuthMTLS:
		return mtlsAuthenticator(cfg.MTLSAllowedClients, o.now)
	}
	return nil, fmt.Errorf("svcruntime: %s must be token or mtls", EnvAuthMode)
}

// tokenAuthenticator validates the platform access token with the skeleton's
// auth_token validators (Ed25519 JWT or PASETO): signature, grant "access", exp
// (required, within AUTH_TOKEN_MAX_LIFETIME, judged with the clock-skew policy), nbf,
// iat, issuer and audience when set, and the required scopes. There is no session
// store in a generated service, so revocation is by expiry unless the service
// supplies a RevocationCheck (WithRevocationCheck).
func tokenAuthenticator(cfg Config, o options) (authenticator, error) {
	if cfg.TokenAnyAudience && cfg.TokenAudience == "" {
		applog.Warningf("svcruntime: %s=true: a token minted for ANY service is accepted here; set %s", EnvAuthAnyAudience, EnvAuthTokenAudience)
	}
	schema := cfg.TokenSchema
	vc := &config.TokenValidatorConfig{
		TokenCommonConfig: config.TokenCommonConfig{
			Enabled:          true,
			SchemaType:       &schema,
			Issuer:           cfg.TokenIssuer,
			Audience:         cfg.TokenAudience,
			ValidateIssuer:   cfg.TokenIssuer != "",
			ValidateAudience: cfg.TokenAudience != "",
		},
		PublicKey: cfg.TokenPublicKey,
	}
	if err := vc.Validate(); err != nil {
		return nil, fmt.Errorf("svcruntime: %s: %w", EnvAuthTokenPublicKey, err)
	}
	v, err := auth_token.NewTokenValidatorWithPolicy(vc, nil, auth_token.TimePolicy{Now: o.now, Leeway: cfg.TokenLeeway})
	if err != nil {
		return nil, fmt.Errorf("svcruntime: token validator: %w", err)
	}
	return func(ctx context.Context) (Caller, error) {
		tok, err := auth_token.ExtractTokenFromContext(ctx, auth_token.TokenAccessName)
		if err != nil {
			return Caller{}, errUnauthenticated
		}
		var claims struct {
			GrantType auth_token.GrantType `json:"grant_type"`
			Exp       json.RawMessage      `json:"exp"`
			Sub       string               `json:"sub"`
			JTI       string               `json:"jti"`
			Scopes    []string             `json:"scopes"`
		}
		// The validators judge exp, nbf and iat with the clock-skew policy.
		if ok, err := v.Verify(*tok, false, &claims); err != nil || !ok || claims.GrantType != auth_token.GrantTypeAccess {
			return Caller{}, errUnauthenticated
		}
		exp, ok := parseExp(claims.Exp)
		if !ok || exp.Sub(o.now()) > cfg.TokenMaxLifetime+cfg.TokenLeeway {
			return Caller{}, errUnauthenticated // no usable exp, or a lifetime beyond the cap
		}
		c := Caller{Subject: claims.Sub, TokenID: claims.JTI, Scopes: claims.Scopes, Expires: exp.Add(cfg.TokenLeeway)}
		if o.revoked != nil {
			if revoked, err := o.revoked(ctx, c); err != nil {
				applog.Errorf("svcruntime: revocation check failed, call refused: %s", errKind(err))
				return Caller{}, errUnauthenticated
			} else if revoked {
				return Caller{}, errUnauthenticated
			}
		}
		return c, nil
	}, nil
}

// maxExpSeconds bounds a numeric exp (year 5138): beyond it the float to int64
// conversion is implementation-defined, and no real token lives that long (C60).
const maxExpSeconds = 1e11

// parseExp reads exp as a JWT NumericDate (whole seconds, 1 to 1e11) or a PASETO
// RFC 3339 string. A fractional, out-of-range or non-numeric value is not usable.
func parseExp(raw json.RawMessage) (time.Time, bool) {
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 && n <= maxExpSeconds && n == math.Trunc(n) {
		return time.Unix(int64(n), 0), true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// mtlsAuthenticator accepts a caller with a verified client certificate whose DNS
// name, URI or (deprecated) subject CN is on the allow-list. A CN entry matches only a
// certificate with no SAN at all.
//
// There is no revocation in mTLS mode: the runtime checks no CRL and no OCSP, so a
// certificate that leaked stays accepted until its NotAfter (or until the CA is
// replaced). Issue short-lived client certificates, and remove a name from
// GRPC_MTLS_ALLOWED_CLIENTS to cut one caller off.
func mtlsAuthenticator(allowed []string, now func() time.Time) (authenticator, error) {
	entries, err := parseAllowedClients(allowed)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.kind == "cn" {
			applog.Warningf("svcruntime: %s has a cn: entry; matching the subject CN is deprecated, use dns: or uri: (SAN)", EnvMTLSAllowedClients)
			break
		}
	}
	return func(ctx context.Context) (Caller, error) {
		p, ok := peer.FromContext(ctx)
		if !ok {
			return Caller{}, errUnauthenticated
		}
		ti, ok := p.AuthInfo.(credentials.TLSInfo)
		if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
			return Caller{}, errUnauthenticated
		}
		leaf := ti.State.VerifiedChains[0][0]
		if !now().Before(leaf.NotAfter) {
			return Caller{}, errCredsExpired
		}
		for _, e := range entries {
			if name := matchLeaf(leaf, e); name != "" {
				return Caller{Subject: name, Expires: leaf.NotAfter}, nil
			}
		}
		return Caller{}, status.Error(codes.PermissionDenied, "this client certificate is not allowed")
	}, nil
}

// matchLeaf returns the certificate name that satisfies entry e, or "".
func matchLeaf(leaf *x509.Certificate, e clientName) string {
	switch e.kind {
	case "dns":
		for _, d := range leaf.DNSNames {
			if strings.EqualFold(d, e.value) {
				return d
			}
		}
	case "uri":
		for _, u := range leaf.URIs {
			if u.String() == e.value {
				return u.String()
			}
		}
	case "cn":
		// A certificate with a SAN names itself there; matching its CN as well would let
		// any holder of a certificate from the shared CA choose a CN it wants to be (C61).
		if hasSAN(leaf) {
			return ""
		}
		if leaf.Subject.CommonName == e.value {
			return leaf.Subject.CommonName
		}
	}
	return ""
}

func hasSAN(c *x509.Certificate) bool {
	return len(c.DNSNames)+len(c.URIs)+len(c.IPAddresses)+len(c.EmailAddresses) > 0
}

// authorize applies the scopes a method requires, on top of authentication.
func authorize(c Caller, fullMethod string, global []string, perMethod map[string][]string) error {
	for _, list := range [][]string{global, perMethod[fullMethod]} {
		for _, want := range list {
			if !slices.Contains(c.Scopes, want) {
				return errScope
			}
		}
	}
	return nil
}

func authUnary(auth authenticator, o options, cfg Config) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if info.FullMethod == healthCheck {
			return h(ctx, req)
		}
		c, err := auth(ctx)
		if err == nil {
			err = authorize(c, info.FullMethod, cfg.TokenRequiredScopes, o.methodScopes)
		}
		if err != nil {
			applog.Warningf("svcruntime: rejected call method=%s code=%s", info.FullMethod, status.Code(err))
			return nil, err
		}
		return h(context.WithValue(ctx, callerKey{}, c), req)
	}
}

func authStream(auth authenticator, o options, cfg Config) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		c, err := auth(ss.Context())
		if err == nil {
			err = authorize(c, info.FullMethod, cfg.TokenRequiredScopes, o.methodScopes)
		}
		if err != nil {
			applog.Warningf("svcruntime: rejected stream method=%s code=%s", info.FullMethod, status.Code(err))
			return err
		}
		// A stream is authenticated once, at open: it must not outlive the credentials.
		// The context ends at the expiry, and every RecvMsg and SendMsg re-checks it.
		ctx, cancel := context.WithDeadline(context.WithValue(ss.Context(), callerKey{}, c), c.Expires)
		defer cancel()
		es := &expiringStream{ServerStream: ss, ctx: ctx, expires: c.Expires, now: o.now}
		err = h(srv, es)
		if err != nil && es.expired() && isExpiryError(err) {
			return errCredsExpired
		}
		return err
	}
}

// isExpiryError reports whether err is what the credential expiry causes inside a
// handler: the expiry error itself or the stream context's deadline. Any other error
// (NotFound, Internal) is the handler's own and stays what it is (C48).
func isExpiryError(err error) bool {
	return errors.Is(err, errCredsExpired) || errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded
}

// expiringStream refuses messages once the caller's credentials have expired.
type expiringStream struct {
	grpc.ServerStream
	ctx     context.Context
	expires time.Time
	now     func() time.Time
}

func (s *expiringStream) Context() context.Context { return s.ctx }
func (s *expiringStream) expired() bool            { return !s.now().Before(s.expires) }

func (s *expiringStream) RecvMsg(m any) error {
	if s.expired() {
		return errCredsExpired
	}
	return s.ServerStream.RecvMsg(m)
}

func (s *expiringStream) SendMsg(m any) error {
	if s.expired() {
		return errCredsExpired
	}
	return s.ServerStream.SendMsg(m)
}

// serverTLS loads the server certificate and, when a client CA is set, requires and
// verifies a client certificate.
func serverTLS(cfg Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("svcruntime: load %s/%s: %w", EnvTLSCertFile, EnvTLSKeyFile, err)
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	if cfg.TLSClientCAFile != "" {
		pem, err := os.ReadFile(cfg.TLSClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("svcruntime: read %s: %w", EnvTLSClientCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("svcruntime: %s holds no PEM certificate", EnvTLSClientCAFile)
		}
		tc.ClientCAs, tc.ClientAuth = pool, tls.RequireAndVerifyClientCert
	}
	return tc, nil
}
