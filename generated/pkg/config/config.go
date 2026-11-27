package config

import (
	"errors"
	"flag"
	"fmt"
	"milton_prism/pkg/log"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	Version   string
	BuildTime string
)

// loadCfg loads a TOML configuration file and decodes it into the provided target struct.
// It accepts a pointer to the target configuration structure (e.g., MicroserviceServerCfg or GatewayServerCfg).
//
// Returns:
//   - An error if there's a failure in reading, decoding, or validating the configuration
func loadCfg(target interface{}) string {
	// Define a command-line flag for specifying the configuration file path.
	// Usage:
	//   ./service --config-file-path=/path/to/config.toml
	// If no path is provided, it defaults to "config.toml".
	configFilePath := flag.String("config-file-path", "config.toml", "Path to the configuration TOML file")
	flag.Parse()

	// Check if the configFilePath pointer is nil (shouldn't happen with `flag.String`)
	if configFilePath == nil {
		log.Fatal("configuration file path pointer is nil: failed to initialize `--config-file-path` flag")
	}

	// Verify that the configuration file exists at the specified path
	fileInfo, err := os.Stat(*configFilePath)
	if os.IsNotExist(err) {
		log.Fatalf("configuration file not found: no file exists at the specified path '%s'", *configFilePath)
	} else if err != nil {
		log.Fatalf("error accessing configuration file: could not access '%s' due to error: %v", *configFilePath, err)
	}

	// Additional check: confirm the specified path is a regular file and not a directory
	if fileInfo.IsDir() {
		log.Fatalf("invalid configuration file path: '%s' is a directory, not a file", *configFilePath)
	}

	// Log the configuration file path being used
	log.Infof("Loading configuration from file: %s", *configFilePath)

	// Decode the TOML configuration into the provided target struct
	if _, err := toml.DecodeFile(*configFilePath, target); err != nil {
		log.Fatalf("TOML decoding error: failed to parse configuration file '%s' - syntax or formatting issue may exist. Details: %v", *configFilePath, err)
	}
	return *configFilePath
}

// DecodeSettingsFile decodes the [settings] section of the file at path into target,
// failing on keys target does not know. Services use it in tests to check that their
// shipped config.toml.example still loads.
func DecodeSettingsFile(path string, target SettingConfig) error { return decodeSettings(path, target) }

// decodeSettings decodes the [settings] section of the file at path into target.
// An absent section leaves target untouched.
func decodeSettings(path string, target SettingConfig) error {
	var raw struct {
		Settings toml.Primitive `toml:"settings"`
	}
	md, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return fmt.Errorf("config: decode [settings] in %s: %w", path, err)
	}
	if !md.IsDefined("settings") {
		return nil
	}
	if err := md.PrimitiveDecode(raw.Settings, target); err != nil {
		return fmt.Errorf("config: decode [settings] in %s: wrong value type", path)
	}
	// A typo in a key must not be ignored silently: name the keys (never the values).
	var unknown []string
	for _, k := range md.Undecoded() {
		if k[0] == "settings" {
			unknown = append(unknown, k.String())
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("config: unknown key(s) %s in %s", strings.Join(unknown, ", "), path)
	}
	return nil
}

// DecodeServiceSection decodes the same config file a prior LoadMicroserviceCfg
// read into target, for a section only one service owns (e.g. migration-services'
// [attestation]) that must not sit on the shared MicroserviceServerCfg. Call it
// after LoadMicroserviceCfg, which parses the --config-file-path flag. A key
// under [section] that target does not know (a typo) is an error, so a
// misspelt secret is never silently ignored. Errors name keys and positions,
// never values: the file holds secrets.
func DecodeServiceSection(section string, target interface{}) error {
	f := flag.Lookup("config-file-path")
	if f == nil || !flag.Parsed() {
		return errors.New("config: DecodeServiceSection called before LoadMicroserviceCfg")
	}
	return decodeSection(f.Value.String(), section, target)
}

func decodeSection(path, section string, target interface{}) error {
	md, err := toml.DecodeFile(path, target)
	if err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			return fmt.Errorf("config: parse %s at line %d", path, perr.Position.Line)
		}
		return fmt.Errorf("config: decode [%s] in %s: wrong value type", section, path)
	}
	for _, k := range md.Undecoded() {
		if k[0] == section {
			return fmt.Errorf("config: unknown key %s in %s", k.String(), path)
		}
	}
	return nil
}

// LoadMicroserviceCfg loads configuration specifically into a GrpcClientCfg struct
// and performs validation on the loaded config.
func LoadMicroserviceCfg(role TokenRole, customConfig SettingConfig) (*MicroserviceServerCfg, error) {
	conf := &MicroserviceServerCfg{
		tokenRole: role,
	}

	if customConfig != nil {
		conf.SettingsConfig = customConfig
	}

	// Use the generic loadCfg function to load into the MicroserviceServerCfg struct
	path := loadCfg(conf)

	// Decode the service-specific [settings] section if the service has one.
	if conf.SettingsConfig != nil {
		if err := decodeSettings(path, conf.SettingsConfig); err != nil {
			return nil, err
		}
	}

	// Verify the loaded configuration
	if err := conf.Validate(); err != nil {
		log.Fatalf("configuration validation error: %v. Please check the configuration file", err)
	}

	// Log basic service information
	log.Info(conf.Server.Name)
	log.Infof("BuildTime: %s", BuildTime)
	log.Infof("Starting service: %s - version %s", conf.Server.Name, Version)

	if conf.Auth != nil {
		err := conf.Auth.Validate(role)
		if err != nil {
			return nil, err
		}

		log.Infof("Algorithm: %#v", conf.Auth.Algorithm)

		if role == TokenRoleValidator {
			log.Infof("Token validator role, schemaType: %#v", *conf.Auth.TokenValidatorConfig.SchemaType)
		} else if role == TokenRoleGenerator {
			log.Infof("Token Generator role, schemaType: %#v", *conf.Auth.TokenGeneratorConfig.SchemaType)
		}
	}

	if conf.Server.ServerOptionCgf != nil {
		log.Infof("MaxRecvMsgSizeMB: %d", conf.Server.MaxRecvMsgSizeMB/(1024*1024))
		log.Infof("MaxSendMsgSizeMB: %d", conf.Server.MaxSendMsgSizeMB/(1024*1024))
	}

	return conf, nil
}

// LoadGatewayCfg loads configuration specifically into a GatewayCfg struct
// and performs validation on the loaded config.
func LoadGatewayCfg() (*GatewayServerCfg, error) {
	var conf GatewayServerCfg

	// Use the generic loadCfg function to load into the GatewayCfg struct
	loadCfg(&conf)

	// Perform any necessary validation on the loaded GatewayServerCfg
	// (Assuming GatewayServerCfg has its own Verify method)
	if err := conf.Validate(); err != nil {
		log.Fatalf("configuration validation error: %v. Please check the configuration file", err)
	}

	// Log basic service information
	log.Info(conf.Server.Name)
	log.Infof("BuildTime: %s", BuildTime)
	log.Infof("Starting service: %s - version %s", conf.Server.Name, Version)

	if conf.Server.ServerOptionCgf != nil {
		log.Infof("MaxRecvMsgSizeMB: %d", conf.Server.MaxRecvMsgSizeMB)
		log.Infof("MaxSendMsgSizeMB: %d", conf.Server.MaxSendMsgSizeMB)
	}

	log.Infof("Cors Enabled: %v", conf.Cors.Enabled)
	if conf.Cors.Enabled {
		log.Infof("Cors AllowedOrigin: %v", conf.Cors.AllowOrigin)
		log.Infof("Cors AllowedMethods: %v", conf.Cors.AllowedMethods)
		log.Infof("Cors AllowedHeaders: %v", conf.Cors.AllowedHeaders)
		log.Infof("Cors ExposeHeaders: %v", conf.Cors.ExposeHeaders)
	}

	return &conf, nil
}

type RequiredFields struct {
	RequireCache              bool
	RequireAuth               bool
	RequireDatabase           bool
	RequireMongoDb            bool
	RequireProfilesSvc        bool
	RequireUsersSvc           bool
	RequireCompaniesSvc       bool
	RequireEmailsSvc          bool
	RequireCommoditiesSvc     bool
	RequireConsignmentNoteSvc bool
	RequireMasterDataSvc      bool
	RequireTradersSvc         bool
}

// SettingConfig defines the interface for service-specific configurations
type SettingConfig interface {
	Validate() error
}

func (c *MicroserviceServerCfg) ValidateWithFlags(flags RequiredFields) error {
	if flags.RequireCache {
		if c.Cache == nil {
			return fmt.Errorf("cache is required")
		}
		if err := c.Cache.Validate(); err != nil {
			return fmt.Errorf("server validation failed: %w", err)
		}
		// The dedicated queue store is optional; validate it only when present
		// so configs still on the single-KeyDB layout keep booting.
		if c.Queue != nil {
			if err := c.Queue.Validate(); err != nil {
				return fmt.Errorf("queue store validation failed: %w", err)
			}
		}
	}

	if flags.RequireAuth {
		if c.Auth == nil {
			return fmt.Errorf("auth is required")
		}
		if err := c.Auth.Validate(c.tokenRole); err != nil {
			return fmt.Errorf("auth validation failed: %w", err)
		}
	}

	if flags.RequireDatabase {
		if c.Database == nil {
			return fmt.Errorf("database is required")
		}
		if err := c.Database.Validate(); err != nil {
			return fmt.Errorf("database validation failed: %w", err)
		}
	}

	if flags.RequireMongoDb {
		if c.Mongo == nil {
			return fmt.Errorf("mongodb is required")
		}
		if err := c.Mongo.Validate(); err != nil {
			return fmt.Errorf("mongodb validation failed: %w", err)
		}
	}

	if flags.RequireProfilesSvc {
		if c.GrpcServices == nil || c.GrpcServices.ProfilesClientConfig == nil {
			return fmt.Errorf("grpc profiles services is required")
		}
		if err := c.GrpcServices.ProfilesClientConfig.Validate(); err != nil {
			return fmt.Errorf("grpc profiles services validation failed: %w", err)
		}
	}

	if flags.RequireUsersSvc {
		if c.GrpcServices == nil || c.GrpcServices.UserClientConfig == nil {
			return fmt.Errorf("grpc users services is required")
		}
		if err := c.GrpcServices.UserClientConfig.Validate(); err != nil {
			return fmt.Errorf("grpc users services validation failed: %w", err)
		}
	}

	if flags.RequireCompaniesSvc {
		if c.GrpcServices == nil || c.GrpcServices.CompaniesClientConfig == nil {
			return fmt.Errorf("grpc companies services is required")
		}
		if err := c.GrpcServices.CompaniesClientConfig.Validate(); err != nil {
			return fmt.Errorf("grpc companies services validation failed: %w", err)
		}
	}

	if flags.RequireEmailsSvc {
		if c.GrpcServices == nil || c.GrpcServices.EmailsClientConfig == nil {
			return fmt.Errorf("grpc emails services is required")
		}
		if err := c.GrpcServices.EmailsClientConfig.Validate(); err != nil {
			return fmt.Errorf("grpc emails services validation failed: %w", err)
		}
	}

	if flags.RequireCommoditiesSvc {
		if c.GrpcServices == nil || c.GrpcServices.CommoditiesClientConfig == nil {
			return fmt.Errorf("grpc commodities services is required")
		}
		if err := c.GrpcServices.CommoditiesClientConfig.Validate(); err != nil {
			return fmt.Errorf("grpc commodities services validation failed: %w", err)
		}
	}

	if flags.RequireMasterDataSvc {
		if c.GrpcServices == nil || c.GrpcServices.MasterDataClientConfig == nil {
			return fmt.Errorf("grpc master data services is required")
		}
		if err := c.GrpcServices.MasterDataClientConfig.Validate(); err != nil {
			return fmt.Errorf("grpc master data services validation failed: %w", err)
		}
	}

	if flags.RequireTradersSvc {
		if c.GrpcServices == nil || c.GrpcServices.TradersClientConfig == nil {
			return fmt.Errorf("grpc traders services is required")
		}
		if err := c.GrpcServices.TradersClientConfig.Validate(); err != nil {
			return fmt.Errorf("grpc traders services validation failed: %w", err)
		}
	}

	if flags.RequireConsignmentNoteSvc {
		if c.GrpcServices == nil || c.GrpcServices.ConsignmentNoteClientConfig == nil {
			return fmt.Errorf("grpc consignment note services is required")
		}
		if err := c.GrpcServices.ConsignmentNoteClientConfig.Validate(); err != nil {
			return fmt.Errorf("grpc consignment note services validation failed: %w", err)
		}
	}

	return nil
}

// MicroserviceServerCfg represents the configuration data stored in the TOML file.
type MicroserviceServerCfg struct {
	Server        *GrpcServerCfg `toml:"microservice"`
	HttpListen    *HttpListenCfg `toml:"http"`
	MetricsListen *HttpListenCfg `toml:"metrics"`
	Cache         *CacheCfg      `toml:"cache"`
	// Queue is the DEDICATED asynq store (noeviction + AOF persistence). It is
	// separate from Cache on purpose: asynq REQUIRES a noeviction policy, whereas
	// the [cache] KeyDB runs allkeys-lru for sessions/cache/pub-sub. When absent,
	// QueueCacheCfg() falls back to Cache for backward compatibility.
	Queue        *CacheCfg       `toml:"queue"`
	Logging      LoggingCfg      `toml:"logging"`
	Auth         *AuthCfg        `toml:"auth"`
	Database     *DatabaseCfg    `toml:"db"`
	Mongo        *MongoDbCfg     `toml:"mongo"`
	GrpcServices *GRPCClientsCfg `toml:"grpcServices"`
	HTTPServices *HTTPClientsCfg `toml:"httpServices"`
	tokenRole    TokenRole

	// SettingsConfig holds the service's own [settings] section, decoded straight
	// into the type the service passes to LoadMicroserviceCfg. There is no untyped
	// map of the raw section: this struct must stay free of any/interface{} so a
	// pointer to it passes the COBOL decimal gate (ADR-0070, R25).
	SettingsConfig SettingConfig `toml:"-"`
}

// Validate checks if the required fields are present and validates optional fields if they are not nil.
func (c *MicroserviceServerCfg) Validate() error {
	// Validate required fields.
	if c.Server == nil {
		return fmt.Errorf("microservice configuration (GrpcServerCfg) is required")
	}

	if err := c.Server.Validate(); err != nil {
		return fmt.Errorf("microservice validation failed: %w", err)
	}

	if c.SettingsConfig != nil {
		if err := c.SettingsConfig.Validate(); err != nil {
			return fmt.Errorf("settings settings_config: %w", err)
		}
	}

	if c.HTTPServices != nil {
		if err := c.HTTPServices.Validate(); err != nil {
			return fmt.Errorf("http services validation failed: %w", err)
		}
	}

	return nil
}

// QueueCacheCfg returns the store that asynq clients/servers must connect to.
// It prefers the dedicated [queue] store (noeviction + AOF) when present and
// falls back to the [cache] store otherwise, so configs without a [queue]
// section keep working exactly as before. Only asynq wiring should use this;
// sessions/cache/pub-sub must keep reading Cache directly.
func (c *MicroserviceServerCfg) QueueCacheCfg() *CacheCfg {
	if c.Queue != nil {
		return c.Queue
	}
	return c.Cache
}

// HttpEnabled checks if the HTTP gateway is properly configured and enabled.
// Returns true only if:
// - HttpListen configuration exists
// - HttpListen is explicitly enabled
// - Required fields are properly set
func (c *MicroserviceServerCfg) HttpEnabled() bool {
	if c.HttpListen == nil {
		return false
	}

	// Additional validation can be added here if needed
	// For example:
	// if c.HttpListen.Port == nil || *c.HttpListen.Port == 0 {
	//     return false
	// }

	return c.HttpListen.Enabled
}

func (c *MicroserviceServerCfg) MetricsEnabled() bool {
	if c.MetricsListen == nil {
		return false
	}

	return c.MetricsListen.Enabled
}

type GatewayServerCfg struct {
	Server       *HttpGateWayServerCfg `toml:"gateway"`
	Cors         *CORSCfg              `toml:"cors"`
	Metrics      MetricsCfg            `toml:"metrics"`
	Logging      LoggingCfg            `toml:"logging"`
	GrpcServices []*GrpcClientCfg      `toml:"grpcServices"`

	// Cache and Auth are OPTIONAL and gate the SSE real-time endpoint
	// (/v1/events) by config-presence. When BOTH are present the gateway mounts
	// the SSE route (KeyDB pub-sub + PASETO verify); when either is absent the
	// gateway behaves exactly as before. Mirror of MicroserviceServerCfg.
	Cache *CacheCfg `toml:"cache"`
	Auth  *AuthCfg  `toml:"auth"`
	// SSE optionally tunes the open-stream caps; zero/absent keeps the defaults
	// (5 per user, 500 total).
	SSE *SSELimitsCfg `toml:"sse"`
}

// SSELimitsCfg caps the open /v1/events streams. Each holds a dedicated keydb
// connection.
type SSELimitsCfg struct {
	MaxStreamsPerOwner int `toml:"maxStreamsPerOwner"`
	MaxStreamsTotal    int `toml:"maxStreamsTotal"`
}

// SSEEnabled reports whether the real-time SSE endpoint should be mounted. The
// feature flag is pure config-presence: both [cache] and [auth] must be set.
func (c *GatewayServerCfg) SSEEnabled() bool {
	return c.Cache != nil && c.Auth != nil
}

// Validate checks the GatewayServerCfg struct for valid configuration values.
func (c *GatewayServerCfg) Validate() error {
	// Verify GrpcServerCfg
	if c.Server == nil {
		return fmt.Errorf("gateway server configuration is required")
	}
	if err := c.Server.Validate(); err != nil {
		return fmt.Errorf("server validation failed: %w", err)
	}

	// Verify CORSCfg
	if c.Cors != nil {
		if err := c.Cors.Validate(); err != nil {
			return fmt.Errorf("cors validation failed: %w", err)
		}
	} else {
		c.Cors = &CORSCfg{}
		_ = c.Cors.Validate()
	}

	// Check that at least one GrpcClientCfg is not nil
	if !c.hasAtLeastOneMicroservice() {
		return fmt.Errorf("at least one microservice configuration is required")
	}

	// Validate each GrpcClientCfg in the slice
	for i, svc := range c.GrpcServices {
		if svc == nil {
			return fmt.Errorf("grpc service at index %d is nil", i)
		}
		if err := svc.Validate(); err != nil {
			return fmt.Errorf("grpc service at index %d validation failed: %w", i, err)
		}
	}

	// SSE feature flag (config-presence). Both fields are OPTIONAL: validate
	// them only when present so a no-SSE gateway config boots exactly as before.
	if c.Auth != nil {
		if err := c.Auth.Validate(TokenRoleValidator); err != nil {
			return fmt.Errorf("gateway auth validation failed: %w", err)
		}
	}
	if c.Cache != nil {
		if err := c.Cache.Validate(); err != nil {
			return fmt.Errorf("gateway cache validation failed: %w", err)
		}
	}

	// Optionally validate MetricsCfg and LoggingCfg fields here if required
	return nil
}

// hasAtLeastOneMicroservice checks if there's at least one configured gRPC service
func (c *GatewayServerCfg) hasAtLeastOneMicroservice() bool {
	if len(c.GrpcServices) == 0 {
		return false
	}
	for _, svc := range c.GrpcServices {
		if svc != nil {
			return true
		}
	}
	return false
}

// intPtr is a helper function to get a pointer to an int value.
func intPtr(i int) *int {
	return &i
}
