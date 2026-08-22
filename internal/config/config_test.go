package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsAreSafe(t *testing.T) {
	d := Defaults()
	if d.InsecureSkipVerify {
		t.Error("TLS verification is disabled by default")
	}
	if d.Timeout <= 0 {
		t.Error("default timeout is not positive; a request could hang forever")
	}
	if d.AdminURL != "" || d.AdminKey != "" {
		t.Error("an endpoint or credential is assumed by default")
	}
	if d.OutputFormat != "text" {
		t.Errorf("default output format = %q, want text", d.OutputFormat)
	}
}

// The whole point of the settings table: the three spellings cannot drift.
func TestEverySettingDeclaresAllSpellings(t *testing.T) {
	seenFlag := map[string]bool{}
	seenEnv := map[string]bool{}
	seenKey := map[string]bool{}

	for _, s := range Settings {
		if s.Key == "" || s.Flag == "" || s.Env == "" || s.Usage == "" {
			t.Errorf("setting %+v is missing a key, flag, env name or usage text", s)
		}
		if seenFlag[s.Flag] {
			t.Errorf("flag --%s is declared twice", s.Flag)
		}
		if seenEnv[s.Env] {
			t.Errorf("environment variable %s%s is declared twice", EnvPrefix, s.Env)
		}
		if seenKey[s.Key] {
			t.Errorf("config key %s is declared twice", s.Key)
		}
		seenFlag[s.Flag], seenEnv[s.Env], seenKey[s.Key] = true, true, true
	}
}

// Every declared key must be reachable through assign, or a setting would be
// silently inert.
func TestEverySettingIsAssignable(t *testing.T) {
	values := map[string]string{
		"admin.insecure_skip_verify": "true",
		"admin.timeout":              "5s",
		"output.format":              "json",
	}
	for _, s := range Settings {
		v, ok := values[s.Key]
		if !ok {
			v = "some-value"
		}
		var cfg Config
		if err := assign(&cfg, s.Key, v); err != nil {
			t.Errorf("assign(%s) failed: %v", s.Key, err)
		}
	}
}

func TestPrecedenceFlagBeatsEnvBeatsFileBeatsDefault(t *testing.T) {
	cfgPath := writeTemp(t, "sixsync.yaml", `
admin:
  url: http://from-file.test:9180
  timeout: 33s
output:
  format: json
`)

	t.Setenv(EnvPrefix+"ADMIN_URL", "http://from-env.test:9180")

	got, err := Resolve(map[string]string{"admin-url": "http://from-flag.test:9180"}, cfgPath)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got.AdminURL != "http://from-flag.test:9180" {
		t.Errorf("AdminURL = %q; the flag should win", got.AdminURL)
	}
	// Set only in the file, so the file value survives.
	if got.Timeout != 33*time.Second {
		t.Errorf("Timeout = %s; the file value should survive when nothing overrides it", got.Timeout)
	}
	if got.OutputFormat != "json" {
		t.Errorf("OutputFormat = %q; the file value should survive", got.OutputFormat)
	}
	// Set nowhere, so the default survives.
	if got.RepoDir != "." {
		t.Errorf("RepoDir = %q; the default should survive", got.RepoDir)
	}
}

func TestEnvBeatsFile(t *testing.T) {
	cfgPath := writeTemp(t, "sixsync.yaml", "admin:\n  url: http://from-file.test:9180\n")
	t.Setenv(EnvPrefix+"ADMIN_URL", "http://from-env.test:9180")

	got, err := Resolve(nil, cfgPath)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.AdminURL != "http://from-env.test:9180" {
		t.Errorf("AdminURL = %q; the environment should beat the file", got.AdminURL)
	}
}

// Operators already have APISIX_* variables in their pipelines.
func TestAPISIXAliasEnvVarsAreAccepted(t *testing.T) {
	t.Setenv("APISIX_ADMIN_API_URL", "http://alias.test:9180")
	t.Setenv("APISIX_ADMIN_API_KEY", "alias-key")

	got, err := Resolve(nil, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.AdminURL != "http://alias.test:9180" {
		t.Errorf("AdminURL = %q, want the APISIX_ alias to be honoured", got.AdminURL)
	}
	if got.AdminKey != "alias-key" {
		t.Errorf("AdminKey was not read from the APISIX_ alias")
	}
}

func TestSixSyncEnvBeatsAPISIXAlias(t *testing.T) {
	t.Setenv("APISIX_ADMIN_API_URL", "http://alias.test:9180")
	t.Setenv(EnvPrefix+"ADMIN_URL", "http://specific.test:9180")

	got, err := Resolve(nil, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.AdminURL != "http://specific.test:9180" {
		t.Errorf("AdminURL = %q; the SixSync-specific variable should win", got.AdminURL)
	}
}

// An unset flag must be absent from the map, not present-and-empty, or it
// would mask the environment and the file.
func TestUnsetFlagsDoNotMaskOtherSources(t *testing.T) {
	t.Setenv(EnvPrefix+"ADMIN_URL", "http://from-env.test:9180")

	got, err := Resolve(map[string]string{}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.AdminURL != "http://from-env.test:9180" {
		t.Errorf("AdminURL = %q; an empty flag map must not mask the environment", got.AdminURL)
	}
}

func TestKeyFileIsRead(t *testing.T) {
	// A trailing newline is what every text editor adds; not stripping it
	// yields a 401 that sends the operator hunting in the wrong place.
	keyPath := writeTemp(t, "key.txt", "secret-admin-key\n")

	got, err := Resolve(map[string]string{"admin-key-file": keyPath}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.AdminKey != "secret-admin-key" {
		t.Errorf("AdminKey = %q, want the file contents with whitespace trimmed", got.AdminKey)
	}
}

func TestKeyAndKeyFileTogetherIsAnError(t *testing.T) {
	keyPath := writeTemp(t, "key.txt", "from-file")
	_, err := Resolve(map[string]string{"admin-key": "inline", "admin-key-file": keyPath}, "")
	if err == nil {
		t.Fatal("expected an error when both a key and a key file are supplied")
	}
}

func TestEmptyKeyFileIsAnError(t *testing.T) {
	keyPath := writeTemp(t, "key.txt", "   \n")
	if _, err := Resolve(map[string]string{"admin-key-file": keyPath}, ""); err == nil {
		t.Fatal("expected an error for an empty key file")
	}
}

func TestMissingKeyFileIsReported(t *testing.T) {
	_, err := Resolve(map[string]string{"admin-key-file": "/nonexistent/key"}, "")
	if err == nil {
		t.Fatal("expected an error for a missing key file")
	}
	if !strings.Contains(err.Error(), "admin key file") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestConfigFileRejectsUnknownFields(t *testing.T) {
	cfgPath := writeTemp(t, "sixsync.yaml", "admin:\n  url: http://x.test\n  typo_field: oops\n")
	if _, err := Resolve(nil, cfgPath); err == nil {
		t.Fatal("expected an error for an unknown config field")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"defaults are valid", func(*Config) {}, false},
		{"bad output format", func(c *Config) { c.OutputFormat = "xml" }, true},
		{"zero timeout", func(c *Config) { c.Timeout = 0 }, true},
		{"negative timeout", func(c *Config) { c.Timeout = -time.Second }, true},
		{"client cert without key", func(c *Config) { c.ClientCertFile = "/c.pem" }, true},
		{"client key without cert", func(c *Config) { c.ClientKeyFile = "/k.pem" }, true},
		{"both cert and key", func(c *Config) { c.ClientCertFile, c.ClientKeyFile = "/c.pem", "/k.pem" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Defaults()
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected an error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestInvalidDurationIsReportedHelpfully(t *testing.T) {
	_, err := Resolve(map[string]string{"timeout": "ten seconds"}, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "10s") {
		t.Errorf("error should suggest a valid form: %v", err)
	}
}

func TestRequireAdminURLNamesEveryWayToSupplyIt(t *testing.T) {
	c := Defaults()
	err := c.RequireAdminURL()
	if err == nil {
		t.Fatal("expected an error when no URL is configured")
	}
	for _, want := range []string{"--admin-url", "SIXSYNC_ADMIN_URL", "APISIX_ADMIN_API_URL", "admin.url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}

	c.AdminURL = "http://x.test"
	if err := c.RequireAdminURL(); err != nil {
		t.Errorf("unexpected error once a URL is set: %v", err)
	}
}

// CI logs are frequently world-readable and long-retained.
func TestStringNeverRevealsTheKey(t *testing.T) {
	const secret = "super-secret-admin-key-value"
	c := Defaults()
	c.AdminKey = secret
	c.AdminURL = "https://gw.example.test:9180"

	got := c.String()
	if strings.Contains(got, secret) {
		t.Fatalf("the admin key leaked into diagnostic output: %s", got)
	}
	if !strings.Contains(got, "sha256:") {
		t.Errorf("expected a key fingerprint for identification: %s", got)
	}
	if !strings.Contains(got, "https://gw.example.test:9180") {
		t.Errorf("non-sensitive settings should still be shown: %s", got)
	}
}

func TestStringDistinguishesUnsetKey(t *testing.T) {
	got := Defaults().String()
	if !strings.Contains(got, "(not set)") {
		t.Errorf("an unset key should be visibly unset rather than fingerprinted: %s", got)
	}
}

func TestUnknownFlagIsRejected(t *testing.T) {
	if _, err := Resolve(map[string]string{"no-such-flag": "x"}, ""); err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
}

func TestMissingConfigFileIsReported(t *testing.T) {
	if _, err := Resolve(nil, "/nonexistent/sixsync.yaml"); err == nil {
		t.Fatal("expected an error when an explicitly named config file is missing")
	}
}
