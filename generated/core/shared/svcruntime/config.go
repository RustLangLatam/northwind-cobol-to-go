// Package svcruntime is the runtime every generated Go gRPC service runs on: the
// gRPC server (TLS, authentication, default deadline, error mapping, limits,
// recovery, logging, metrics, tracing), the probe and metrics listener, the store
// lifecycle, the business clock and the run loop with ordered shutdown.
//
// A service calls ConfigFromEnv, NewServer, registers its handlers on Server.GRPC,
// sets its readiness check and calls Run. It never builds a bare grpc.NewServer,
// never handles signals and never writes an exporter. ADR-0070.
//
// The package is skeleton code: the COBOL decimal gate pins it byte for byte, so an
// agent cannot change it, and it does not count as the agent's code.
package svcruntime

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"milton_prism/core/shared/auth_token"
)

// Token policy defaults (ADR-0070).
const (
	// DefaultTokenLeeway is the clock skew tolerated with the token issuer; the
	// maximum is auth_token.MaxLeeway (2m).
	DefaultTokenLeeway = 30 * time.Second
	// DefaultTokenMaxLifetime bounds how far in the future a token's exp may be. A
	// generated service has no session store, so expiry is the only revocation: a
	// token that lives for days is a credential that cannot be taken back.
	DefaultTokenMaxLifetime = 24 * time.Hour
	// MaxTokenLifetime is the ceiling of AUTH_TOKEN_MAX_LIFETIME (C59): a token that
	// lives longer than a month, with revocation by expiry only, is not a bounded risk.
	MaxTokenLifetime = 30 * 24 * time.Hour
)

// Environment variables read by ConfigFromEnv. Every one of them belongs in the
// service's .env.example and deploy config.
const (
	EnvGRPCHost                    = "GRPC_HOST"                          // default 0.0.0.0 with TLS, 127.0.0.1 without
	EnvGRPCPort                    = "GRPC_PORT"                          // default 50051
	EnvTLSCertFile                 = "GRPC_TLS_CERT_FILE"                 // server certificate (PEM)
	EnvTLSKeyFile                  = "GRPC_TLS_KEY_FILE"                  // server key (PEM)
	EnvTLSClientCAFile             = "GRPC_TLS_CLIENT_CA_FILE"            // set: clients must present a certificate it signed (mTLS)
	EnvAllowPlaintext              = "GRPC_ALLOW_PLAINTEXT"               // "true": serve without TLS (local development only), bound to loopback
	EnvAllowPlaintextRemote        = "GRPC_ALLOW_PLAINTEXT_REMOTE"        // "true": with plaintext, also allow a non-loopback GRPC_HOST (bearer tokens travel in clear)
	EnvAuthMode                    = "GRPC_AUTH_MODE"                     // "token" or "mtls"; required
	EnvAuthTokenSchema             = "AUTH_TOKEN_SCHEMA"                  // "JWT" (default) or "Paseto"
	EnvAuthTokenPublicKey          = "AUTH_TOKEN_PUBLIC_KEY"              // Ed25519 public key, 64 hex characters (not a secret)
	EnvAuthTokenIssuer             = "AUTH_TOKEN_ISSUER"                  // set: the token issuer must match
	EnvAuthTokenAudience           = "AUTH_TOKEN_AUDIENCE"                // the token audience must match; required in token mode
	EnvAuthAnyAudience             = "AUTH_TOKEN_ALLOW_ANY_AUDIENCE"      // "true": accept a token for any audience (explicit opt-out)
	EnvAuthTokenClockSkew          = "AUTH_TOKEN_CLOCK_SKEW"              // Go duration, default 30s, at most 2m: tolerated clock difference with the issuer
	EnvAuthTokenMaxLifetime        = "AUTH_TOKEN_MAX_LIFETIME"            // Go duration, default 24h, at most 720h (30 days): a token that expires later than this from now is refused
	EnvAuthTokenRequiredScopes     = "AUTH_TOKEN_REQUIRED_SCOPES"         // comma list: every token must carry all of these scopes (token mode)
	EnvMTLSAllowedClients          = "GRPC_MTLS_ALLOWED_CLIENTS"          // required in mtls mode: comma list of dns:NAME, uri:URI or cn:NAME (deprecated) entries
	EnvDefaultTimeout              = "GRPC_DEFAULT_TIMEOUT"               // Go duration, default 30s, applied when the caller sends no deadline
	EnvMaxConcurrentStreams        = "GRPC_MAX_CONCURRENT_STREAMS"        // default 100
	EnvMaxRecvMsgBytes             = "GRPC_MAX_RECV_MSG_BYTES"            // default 4194304
	EnvMetricsHost                 = "METRICS_HOST"                       // default 127.0.0.1; a deploy that scrapes from outside the pod sets 0.0.0.0
	EnvMetricsPort                 = "METRICS_PORT"                       // default 9100
	EnvMetricsTokenFile            = "METRICS_BEARER_TOKEN_FILE"          // file holding a bearer token (16+ bytes) required on /metrics and the service's HandleHTTP routes; sent in cleartext (plain HTTP)
	EnvMetricsAllowUnauthenticated = "METRICS_ALLOW_UNAUTHENTICATED"      // "true": a METRICS_HOST beyond loopback with no token file (the pod network must protect it)
	EnvShutdownTimeout             = "SHUTDOWN_TIMEOUT"                   // Go duration, default 15s, bound of the graceful stop
	EnvReadyCacheTTL               = "READYZ_CACHE_TTL"                   // Go duration, default 2s, how long a readiness result is reused
	EnvOTelServiceName             = "OTEL_SERVICE_NAME"                  // default: the service name
	EnvOTelEndpoint                = "OTEL_EXPORTER_OTLP_ENDPOINT"        // empty: tracing off
	EnvOTelInsecure                = "OTEL_EXPORTER_OTLP_INSECURE"        // "true": allow http:// to a non-loopback collector
	EnvOTelTracesEndpoint          = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT" // NOT supported: set OTEL_EXPORTER_OTLP_ENDPOINT; a value here only logs a warning
)

// AuthMode is how the server authenticates callers. There is no "none".
type AuthMode string

const (
	// AuthToken validates the platform access token (Ed25519 JWT or PASETO) in the
	// "authorization" metadata: signature, expiry, grant "access", issuer and
	// audience when configured.
	AuthToken AuthMode = "token"
	// AuthMTLS accepts a caller whose client certificate the configured CA signed
	// (and whose name is on the allow-list when one is set).
	AuthMTLS AuthMode = "mtls"
)

// Config is the runtime configuration, read from the environment by ConfigFromEnv.
type Config struct {
	ServiceName          string
	GRPCAddr             string
	TLSCertFile          string
	TLSKeyFile           string
	TLSClientCAFile      string
	AllowPlaintext       bool
	AuthMode             AuthMode
	TokenSchema          string
	TokenPublicKey       string
	TokenIssuer          string
	TokenAudience        string
	TokenAnyAudience     bool
	TokenLeeway          time.Duration
	TokenMaxLifetime     time.Duration
	TokenRequiredScopes  []string
	MTLSAllowedClients   []string
	AllowPlaintextRemote bool
	MetricsTokenFile     string
	// MetricsAllowUnauthenticated opts in to a metrics listener beyond loopback with
	// no token file (C57); Validate refuses that combination otherwise.
	MetricsAllowUnauthenticated bool
	OTelTracesIgnored           bool
	DefaultTimeout              time.Duration
	MaxConcurrentStreams        uint32
	MaxRecvMsgBytes             int
	MetricsAddr                 string
	ShutdownTimeout             time.Duration
	ReadyCacheTTL               time.Duration
	OTelServiceName             string
	OTelEndpoint                string
	OTelInsecure                bool
}

// ConfigFromEnv reads the runtime configuration for service. Errors name the
// variable, never its value.
func ConfigFromEnv(service string, getenv func(string) string) (Config, error) {
	if strings.TrimSpace(service) == "" {
		return Config{}, errors.New("svcruntime: the service name is empty")
	}
	e := envReader{get: getenv}
	// The default bind is the pod address with TLS and loopback without it.
	defHost := "0.0.0.0"
	if e.str(EnvTLSCertFile, "") == "" {
		defHost = "127.0.0.1"
	}
	cfg := Config{
		ServiceName:                 service,
		GRPCAddr:                    net.JoinHostPort(e.str(EnvGRPCHost, defHost), e.str(EnvGRPCPort, "50051")),
		TLSCertFile:                 e.str(EnvTLSCertFile, ""),
		TLSKeyFile:                  e.str(EnvTLSKeyFile, ""),
		TLSClientCAFile:             e.str(EnvTLSClientCAFile, ""),
		AllowPlaintext:              e.boolean(EnvAllowPlaintext),
		AllowPlaintextRemote:        e.boolean(EnvAllowPlaintextRemote),
		AuthMode:                    AuthMode(strings.ToLower(e.str(EnvAuthMode, ""))),
		TokenSchema:                 e.str(EnvAuthTokenSchema, "JWT"),
		TokenPublicKey:              e.str(EnvAuthTokenPublicKey, ""),
		TokenIssuer:                 e.str(EnvAuthTokenIssuer, ""),
		TokenAudience:               e.str(EnvAuthTokenAudience, ""),
		TokenAnyAudience:            e.boolean(EnvAuthAnyAudience),
		TokenLeeway:                 e.durationOrZero(EnvAuthTokenClockSkew, DefaultTokenLeeway),
		TokenMaxLifetime:            e.duration(EnvAuthTokenMaxLifetime, DefaultTokenMaxLifetime),
		TokenRequiredScopes:         e.list(EnvAuthTokenRequiredScopes),
		MTLSAllowedClients:          e.list(EnvMTLSAllowedClients),
		MetricsTokenFile:            e.str(EnvMetricsTokenFile, ""),
		MetricsAllowUnauthenticated: e.boolean(EnvMetricsAllowUnauthenticated),
		OTelTracesIgnored:           e.str(EnvOTelTracesEndpoint, "") != "",
		DefaultTimeout:              e.duration(EnvDefaultTimeout, 30*time.Second),
		MetricsAddr:                 net.JoinHostPort(e.str(EnvMetricsHost, "127.0.0.1"), e.str(EnvMetricsPort, "9100")),
		ShutdownTimeout:             e.duration(EnvShutdownTimeout, 15*time.Second),
		ReadyCacheTTL:               e.duration(EnvReadyCacheTTL, 2*time.Second),
		OTelServiceName:             e.str(EnvOTelServiceName, service),
		OTelEndpoint:                e.str(EnvOTelEndpoint, ""),
		OTelInsecure:                e.boolean(EnvOTelInsecure),
	}
	cfg.MaxConcurrentStreams = uint32(e.positive(EnvMaxConcurrentStreams, 100, 1<<20))
	cfg.MaxRecvMsgBytes = e.positive(EnvMaxRecvMsgBytes, 4<<20, 64<<20)
	if e.err != nil {
		return Config{}, e.err
	}
	return cfg, cfg.Validate()
}

// Validate checks that the configuration is safe to serve with.
func (c Config) Validate() error {
	hasCert, hasKey := c.TLSCertFile != "", c.TLSKeyFile != ""
	switch {
	case hasCert != hasKey:
		return fmt.Errorf("svcruntime: %s and %s must be set together", EnvTLSCertFile, EnvTLSKeyFile)
	case !hasCert && !c.AllowPlaintext:
		return fmt.Errorf("svcruntime: %s and %s are required; set %s=true only for local development", EnvTLSCertFile, EnvTLSKeyFile, EnvAllowPlaintext)
	case !hasCert && c.TLSClientCAFile != "":
		return fmt.Errorf("svcruntime: %s needs %s and %s", EnvTLSClientCAFile, EnvTLSCertFile, EnvTLSKeyFile)
	case !hasCert && !c.AllowPlaintextRemote && !loopbackAddr(c.GRPCAddr):
		return fmt.Errorf("svcruntime: without TLS the server binds to loopback only (bearer tokens would travel in clear); set %s=true to bind %s elsewhere", EnvAllowPlaintextRemote, EnvGRPCHost)
	case c.MetricsTokenFile == "" && !c.MetricsAllowUnauthenticated && !loopbackAddr(c.MetricsAddr):
		return fmt.Errorf("svcruntime: %s is not loopback and %s is unset: /metrics and the HandleHTTP routes would be open to anything that reaches the pod; set %s to a token file, or %s=true when a NetworkPolicy or mesh protects the listener", EnvMetricsHost, EnvMetricsTokenFile, EnvMetricsTokenFile, EnvMetricsAllowUnauthenticated)
	}
	switch c.AuthMode {
	case AuthToken:
		if c.TokenPublicKey == "" {
			return fmt.Errorf("svcruntime: %s=token needs %s", EnvAuthMode, EnvAuthTokenPublicKey)
		}
		if c.TokenSchema != "JWT" && c.TokenSchema != "Paseto" {
			return fmt.Errorf("svcruntime: %s must be JWT or Paseto", EnvAuthTokenSchema)
		}
		if c.TokenAudience == "" && !c.TokenAnyAudience {
			return fmt.Errorf("svcruntime: %s=token needs %s (or %s=true)", EnvAuthMode, EnvAuthTokenAudience, EnvAuthAnyAudience)
		}
		if len(c.MTLSAllowedClients) > 0 {
			return fmt.Errorf("svcruntime: %s applies to %s=mtls only", EnvMTLSAllowedClients, EnvAuthMode)
		}
		if c.TokenLeeway < 0 || c.TokenLeeway > auth_token.MaxLeeway {
			return fmt.Errorf("svcruntime: %s must be between 0 and %s", EnvAuthTokenClockSkew, auth_token.MaxLeeway)
		}
		if c.TokenMaxLifetime <= 0 || c.TokenMaxLifetime > MaxTokenLifetime {
			return fmt.Errorf("svcruntime: %s must be positive and at most %s", EnvAuthTokenMaxLifetime, MaxTokenLifetime)
		}
	case AuthMTLS:
		if c.TLSClientCAFile == "" {
			return fmt.Errorf("svcruntime: %s=mtls needs %s", EnvAuthMode, EnvTLSClientCAFile)
		}
		if _, err := parseAllowedClients(c.MTLSAllowedClients); err != nil {
			return err
		}
		if len(c.TokenRequiredScopes) > 0 {
			return fmt.Errorf("svcruntime: %s applies to %s=token only", EnvAuthTokenRequiredScopes, EnvAuthMode)
		}
	default:
		return fmt.Errorf("svcruntime: %s must be token or mtls", EnvAuthMode)
	}
	if c.DefaultTimeout <= 0 || c.ShutdownTimeout <= 0 || c.ReadyCacheTTL <= 0 {
		return errors.New("svcruntime: timeouts must be positive")
	}
	return nil
}

type envReader struct {
	get func(string) string
	err error
}

func (e *envReader) str(key, def string) string {
	if v := strings.TrimSpace(e.get(key)); v != "" {
		return v
	}
	return def
}

func (e *envReader) fail(key, want string) {
	if e.err == nil {
		e.err = fmt.Errorf("svcruntime: %s must be %s", key, want)
	}
}

func (e *envReader) boolean(key string) bool {
	v := e.str(key, "")
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail(key, "true or false")
	}
	return b
}

func (e *envReader) duration(key string, def time.Duration) time.Duration {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.fail(key, "a positive Go duration such as 30s")
		return def
	}
	return d
}

// durationOrZero is duration, except that 0 (or 0s) is a valid value.
func (e *envReader) durationOrZero(key string, def time.Duration) time.Duration {
	if v := e.str(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d == 0 {
			return 0
		}
	}
	return e.duration(key, def)
}

func (e *envReader) positive(key string, def, max int) int {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > max {
		e.fail(key, fmt.Sprintf("an integer from 1 to %d", max))
		return def
	}
	return n
}

func (e *envReader) list(key string) []string {
	var out []string
	for _, p := range strings.Split(e.str(key, ""), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loopbackAddr reports whether the host of a host:port address is loopback.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && isLoopback(host)
}

// clientName is one typed GRPC_MTLS_ALLOWED_CLIENTS entry.
type clientName struct{ kind, value string }

// parseAllowedClients reads dns:NAME, uri:URI and cn:NAME entries. The list is
// required: an mtls server that accepts every certificate its CA signed lets any
// holder of such a certificate call it.
func parseAllowedClients(list []string) ([]clientName, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("svcruntime: %s is required with %s=mtls (dns:NAME, uri:URI or cn:NAME entries)", EnvMTLSAllowedClients, EnvAuthMode)
	}
	out := make([]clientName, 0, len(list))
	for _, item := range list {
		kind, val, ok := strings.Cut(item, ":")
		kind = strings.ToLower(kind)
		if !ok || val == "" || (kind != "dns" && kind != "uri" && kind != "cn") {
			return nil, fmt.Errorf("svcruntime: every %s entry must be dns:NAME, uri:URI or cn:NAME (cn is deprecated)", EnvMTLSAllowedClients)
		}
		out = append(out, clientName{kind, val})
	}
	return out, nil
}

// EnvNames lists every environment variable the runtime reads (ConfigFromEnv and
// ClockFromEnv), for the service's .env.example and the generation prompt.
func EnvNames() []string {
	return []string{
		EnvGRPCHost, EnvGRPCPort, EnvTLSCertFile, EnvTLSKeyFile, EnvTLSClientCAFile, EnvAllowPlaintext,
		EnvAuthMode, EnvAuthTokenSchema, EnvAuthTokenPublicKey, EnvAuthTokenIssuer, EnvAuthTokenAudience, EnvAuthAnyAudience,
		EnvAllowPlaintextRemote, EnvAuthTokenClockSkew, EnvAuthTokenMaxLifetime, EnvAuthTokenRequiredScopes,
		EnvMTLSAllowedClients, EnvDefaultTimeout, EnvMaxConcurrentStreams, EnvMaxRecvMsgBytes,
		EnvMetricsHost, EnvMetricsPort, EnvMetricsTokenFile, EnvMetricsAllowUnauthenticated, EnvShutdownTimeout, EnvReadyCacheTTL,
		EnvOTelServiceName, EnvOTelEndpoint, EnvOTelInsecure, EnvOTelTracesEndpoint, EnvServiceTimeZone,
	}
}
