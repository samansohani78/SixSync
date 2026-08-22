// Package config resolves SixSync's settings from flags, environment variables
// and a configuration file.
//
// Every setting is declared once, in the table below, and the flag name,
// environment variable and file key are all derived from that single entry.
// Declaring them separately is how the three drift apart until an environment
// variable silently stops working.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/samansohani78/SixSync/internal/redact"
)

// Config is the resolved settings for one command invocation.
type Config struct {
	// AdminURL is the APISIX Admin API endpoint.
	AdminURL string

	// AdminKey is the Admin API credential. Never printed; see String.
	AdminKey string

	// AdminKeyFile reads the credential from a file instead.
	//
	// Preferred over passing the key directly: a file can be mounted with
	// restrictive permissions, and unlike an environment variable it is not
	// visible in /proc or inherited by child processes.
	AdminKeyFile string

	// CAFile verifies the server against a private CA.
	CAFile string

	// ClientCertFile and ClientKeyFile enable mTLS to the Admin API.
	ClientCertFile string
	ClientKeyFile  string

	// InsecureSkipVerify disables TLS verification. Strongly discouraged; the
	// CLI warns whenever it is set.
	InsecureSkipVerify bool

	// Timeout bounds each Admin API request.
	Timeout time.Duration

	// OutputFormat is "text" or "json".
	OutputFormat string

	// RepoDir is the directory holding resource files.
	RepoDir string
}

// Defaults are the values used when nothing else supplies one.
//
// Every default is safe: TLS verification on, a bounded timeout, and no
// endpoint or credential assumed.
func Defaults() Config {
	return Config{
		Timeout:      10 * time.Second,
		OutputFormat: "text",
		RepoDir:      ".",
	}
}

// EnvPrefix is SixSync's own environment variable prefix.
const EnvPrefix = "SIXSYNC_"

// setting declares one configuration key and its three spellings.
type setting struct {
	// Key is the dotted name used in the configuration file.
	Key string
	// Flag is the command-line flag name.
	Flag string
	// Env is the SixSync environment variable, without the prefix.
	Env string
	// AltEnv is an additional accepted variable, used for the APISIX_* names
	// that operators already have in their pipelines.
	AltEnv string
	// Usage is the flag's help text.
	Usage string
	// Sensitive marks a value that must never be printed.
	Sensitive bool
}

// Settings is the single declaration of every configuration key.
var Settings = []setting{
	{Key: "admin.url", Flag: "admin-url", Env: "ADMIN_URL", AltEnv: "APISIX_ADMIN_API_URL",
		Usage: "APISIX Admin API base URL"},
	{Key: "admin.key", Flag: "admin-key", Env: "ADMIN_KEY", AltEnv: "APISIX_ADMIN_API_KEY",
		Usage: "APISIX Admin API key (prefer --admin-key-file)", Sensitive: true},
	{Key: "admin.key_file", Flag: "admin-key-file", Env: "ADMIN_KEY_FILE", AltEnv: "APISIX_ADMIN_API_KEY_FILE",
		Usage: "file containing the Admin API key"},
	{Key: "admin.ca_file", Flag: "ca-file", Env: "CA_FILE", AltEnv: "APISIX_CA_FILE",
		Usage: "PEM CA bundle used to verify the Admin API server"},
	{Key: "admin.client_cert_file", Flag: "client-cert-file", Env: "CLIENT_CERT_FILE", AltEnv: "APISIX_CLIENT_CERT_FILE",
		Usage: "client certificate for Admin API mTLS"},
	{Key: "admin.client_key_file", Flag: "client-key-file", Env: "CLIENT_KEY_FILE", AltEnv: "APISIX_CLIENT_KEY_FILE",
		Usage: "client private key for Admin API mTLS"},
	{Key: "admin.insecure_skip_verify", Flag: "insecure-skip-tls-verify", Env: "INSECURE_SKIP_TLS_VERIFY",
		Usage: "disable TLS verification (strongly discouraged)"},
	{Key: "admin.timeout", Flag: "timeout", Env: "TIMEOUT",
		Usage: "per-request timeout for Admin API calls"},
	{Key: "output.format", Flag: "output", Env: "OUTPUT",
		Usage: "output format: text or json"},
	{Key: "repo.dir", Flag: "repo-dir", Env: "REPO_DIR",
		Usage: "directory containing SixSync resource files"},
}

// file mirrors the on-disk configuration file.
type file struct {
	Admin struct {
		URL                string `yaml:"url"`
		Key                string `yaml:"key"`
		KeyFile            string `yaml:"key_file"`
		CAFile             string `yaml:"ca_file"`
		ClientCertFile     string `yaml:"client_cert_file"`
		ClientKeyFile      string `yaml:"client_key_file"`
		InsecureSkipVerify *bool  `yaml:"insecure_skip_verify"`
		Timeout            string `yaml:"timeout"`
	} `yaml:"admin"`
	Output struct {
		Format string `yaml:"format"`
	} `yaml:"output"`
	Repo struct {
		Dir string `yaml:"dir"`
	} `yaml:"repo"`
}

// Resolve merges every source in precedence order:
//
//	command-line flags  >  environment  >  configuration file  >  defaults
//
// Flags win because they are the most explicit statement of intent for a single
// run. The file ranks lowest of the three because it is checked into a
// repository and shared, so a local environment must be able to override it
// without editing a tracked file.
//
// flags holds only those values a user actually set; unset flags must be
// absent from the map rather than present-and-empty, or they would mask lower
// precedence sources.
func Resolve(flags map[string]string, configPath string) (Config, error) {
	cfg := Defaults()

	if configPath != "" {
		if err := applyFile(&cfg, configPath); err != nil {
			return cfg, err
		}
	}
	if err := applyEnv(&cfg, os.Getenv); err != nil {
		return cfg, err
	}
	if err := applyFlags(&cfg, flags); err != nil {
		return cfg, err
	}

	if err := cfg.loadKeyFile(); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func applyFile(cfg *Config, path string) error {
	// The path is supplied by the operator via --config; reading it is the
	// point of this function.
	data, err := os.ReadFile(path) //nolint:gosec // G304: intentional
	if err != nil {
		return fmt.Errorf("reading config file %s: %w", path, err)
	}

	var f file
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return fmt.Errorf("parsing config file %s: %w", path, err)
	}

	setString(&cfg.AdminURL, f.Admin.URL)
	setString(&cfg.AdminKey, f.Admin.Key)
	setString(&cfg.AdminKeyFile, f.Admin.KeyFile)
	setString(&cfg.CAFile, f.Admin.CAFile)
	setString(&cfg.ClientCertFile, f.Admin.ClientCertFile)
	setString(&cfg.ClientKeyFile, f.Admin.ClientKeyFile)
	setString(&cfg.OutputFormat, f.Output.Format)
	setString(&cfg.RepoDir, f.Repo.Dir)

	if f.Admin.InsecureSkipVerify != nil {
		cfg.InsecureSkipVerify = *f.Admin.InsecureSkipVerify
	}
	if f.Admin.Timeout != "" {
		d, err := time.ParseDuration(f.Admin.Timeout)
		if err != nil {
			return fmt.Errorf("config file %s: admin.timeout: %w", path, err)
		}
		cfg.Timeout = d
	}
	return nil
}

// lookupFunc reads an environment variable; injected so tests need not mutate
// the real process environment.
type lookupFunc func(string) string

func applyEnv(cfg *Config, getenv lookupFunc) error {
	// read returns the SixSync-prefixed variable if set, otherwise the
	// APISIX_* alias. SIXSYNC_* wins so that a SixSync-specific setting can
	// override a shared APISIX one already present in a pipeline.
	read := func(s setting) string {
		if v := getenv(EnvPrefix + s.Env); v != "" {
			return v
		}
		if s.AltEnv != "" {
			return getenv(s.AltEnv)
		}
		return ""
	}

	for _, s := range Settings {
		v := read(s)
		if v == "" {
			continue
		}
		if err := assign(cfg, s.Key, v); err != nil {
			name := EnvPrefix + s.Env
			return fmt.Errorf("environment variable %s: %w", name, err)
		}
	}
	return nil
}

func applyFlags(cfg *Config, flags map[string]string) error {
	byFlag := make(map[string]setting, len(Settings))
	for _, s := range Settings {
		byFlag[s.Flag] = s
	}
	for name, v := range flags {
		s, ok := byFlag[name]
		if !ok {
			return fmt.Errorf("unknown configuration flag --%s", name)
		}
		if err := assign(cfg, s.Key, v); err != nil {
			return fmt.Errorf("--%s: %w", name, err)
		}
	}
	return nil
}

// assign writes one value by its canonical key, so that all three sources
// share a single conversion and validation path.
func assign(cfg *Config, key, value string) error {
	switch key {
	case "admin.url":
		cfg.AdminURL = value
	case "admin.key":
		cfg.AdminKey = value
	case "admin.key_file":
		cfg.AdminKeyFile = value
	case "admin.ca_file":
		cfg.CAFile = value
	case "admin.client_cert_file":
		cfg.ClientCertFile = value
	case "admin.client_key_file":
		cfg.ClientKeyFile = value
	case "admin.insecure_skip_verify":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes":
			cfg.InsecureSkipVerify = true
		case "false", "0", "no", "":
			cfg.InsecureSkipVerify = false
		default:
			return fmt.Errorf("%q is not a boolean", value)
		}
	case "admin.timeout":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("%q is not a duration (try 10s): %w", value, err)
		}
		cfg.Timeout = d
	case "output.format":
		cfg.OutputFormat = value
	case "repo.dir":
		cfg.RepoDir = value
	default:
		return fmt.Errorf("unknown configuration key %q", key)
	}
	return nil
}

func setString(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// loadKeyFile reads the credential from disk when a key file is configured.
//
// The trailing newline a text editor adds is stripped: an Admin API key with a
// newline appended is simply the wrong key, and the resulting 401 sends the
// operator hunting in the wrong place.
func (c *Config) loadKeyFile() error {
	if c.AdminKeyFile == "" {
		return nil
	}
	if c.AdminKey != "" {
		return errors.New("both an Admin API key and a key file were provided; supply only one")
	}
	data, err := os.ReadFile(c.AdminKeyFile)
	if err != nil {
		return fmt.Errorf("reading admin key file: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return fmt.Errorf("admin key file %s is empty", c.AdminKeyFile)
	}
	c.AdminKey = key
	return nil
}

// Validate checks settings that are wrong regardless of which source set them.
func (c *Config) Validate() error {
	switch c.OutputFormat {
	case "text", "json":
	default:
		return fmt.Errorf("output format %q is not supported; use text or json", c.OutputFormat)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive, got %s", c.Timeout)
	}
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") {
		return errors.New("client certificate and client key must be provided together")
	}
	return nil
}

// RequireAdminURL reports a clear error when a command needs an endpoint and
// none was configured, naming every way to supply one.
func (c *Config) RequireAdminURL() error {
	if strings.TrimSpace(c.AdminURL) != "" {
		return nil
	}
	return fmt.Errorf("no Admin API URL configured; set --admin-url, %sADMIN_URL, "+
		"APISIX_ADMIN_API_URL, or admin.url in the config file", EnvPrefix)
}

// String renders the configuration for diagnostics with the credential
// replaced by a fingerprint.
//
// The fingerprint lets an operator confirm which key a pipeline picked up
// without the value appearing in CI logs, which are frequently world-readable
// and retained long after the run.
func (c Config) String() string {
	key := "(not set)"
	if c.AdminKey != "" {
		key = redact.Fingerprint(c.AdminKey)
	}
	return fmt.Sprintf(
		"AdminURL=%s AdminKey=%s AdminKeyFile=%s CAFile=%s ClientCertFile=%s "+
			"ClientKeyFile=%s InsecureSkipVerify=%t Timeout=%s OutputFormat=%s RepoDir=%s",
		c.AdminURL, key, c.AdminKeyFile, c.CAFile, c.ClientCertFile,
		c.ClientKeyFile, c.InsecureSkipVerify, c.Timeout, c.OutputFormat, c.RepoDir,
	)
}
