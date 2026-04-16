// Package config handles loading and resolving proxy configuration from a YAML file.
//
// Environment variable overrides (applied after file is parsed):
//   - PROVIDER     — overrides the 'active' field
//   - UPSTREAM_URL — overrides the active provider's upstream URL
//   - LISTEN_ADDR  — overrides the top-level listen address
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"anthropic-proxy/internal/provider"

	"gopkg.in/yaml.v3"
)

// Default retry values applied when a rule omits the field.
const (
	defaultListenAddr  = ":8080"
	defaultMaxRetries  = 10
	defaultRetryDelay  = 2 * time.Second
	defaultRetryJitter = 1 * time.Second
)

// Config is the resolved runtime configuration for the active provider.
type Config struct {
	ListenAddr    string
	Upstream      string
	ProviderName  string
	OverloadRules []provider.Rule
	Logging       LoggingConfig
}

// LoggingConfig holds logging-related configuration.
type LoggingConfig struct {
	Enabled      bool
	DatabasePath string
	MaxAgeDays   int
}

// ---- YAML types ----

type yamlDuration struct{ time.Duration }

func (d *yamlDuration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	d.Duration = dur
	return nil
}

type ruleYAML struct {
	Status       int           `yaml:"status"`
	BodyContains string        `yaml:"body_contains"`
	MaxRetries   *int          `yaml:"max_retries"`
	Delay        *yamlDuration `yaml:"delay"`
	Jitter       *yamlDuration `yaml:"jitter"`
}

type providerYAML struct {
	Upstream      string     `yaml:"upstream"`
	OverloadRules []ruleYAML `yaml:"overload_rules"`
}

type loggingYAML struct {
	Enabled      *bool  `yaml:"enabled"`
	DatabasePath string `yaml:"database_path"`
	MaxAgeDays   *int   `yaml:"max_age_days"`
}

type fileConfig struct {
	Listen    string                  `yaml:"listen"`
	Active    string                  `yaml:"active"`
	Logging   loggingYAML             `yaml:"logging"`
	Providers map[string]providerYAML `yaml:"providers"`
}

// Load reads the YAML config file at path and returns the resolved Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	if v := os.Getenv("PROVIDER"); v != "" {
		fc.Active = v
	}
	if fc.Active == "" {
		return nil, fmt.Errorf("config: set 'active' in config.yaml or via PROVIDER env var")
	}

	pc, ok := fc.Providers[fc.Active]
	if !ok {
		return nil, fmt.Errorf("config: provider %q not found in config.yaml", fc.Active)
	}

	if v := os.Getenv("UPSTREAM_URL"); v != "" {
		pc.Upstream = v
	}

	return resolve(fc.Active, pc, fc.Listen, fc.Logging)
}

func resolve(name string, pc providerYAML, listen string, lc loggingYAML) (*Config, error) {
	if pc.Upstream == "" {
		return nil, fmt.Errorf("provider %q: upstream URL is required", name)
	}
	if len(pc.OverloadRules) == 0 {
		return nil, fmt.Errorf("provider %q: overload_rules must not be empty", name)
	}

	if listen == "" {
		listen = defaultListenAddr
	}

	rules := make([]provider.Rule, len(pc.OverloadRules))
	for i, r := range pc.OverloadRules {
		rule := provider.Rule{
			Status:       r.Status,
			BodyContains: r.BodyContains,
			MaxRetries:   defaultMaxRetries,
			RetryDelay:   defaultRetryDelay,
			RetryJitter:  defaultRetryJitter,
		}
		if r.MaxRetries != nil {
			rule.MaxRetries = *r.MaxRetries
		}
		if r.Delay != nil {
			rule.RetryDelay = r.Delay.Duration
		}
		if r.Jitter != nil {
			rule.RetryJitter = r.Jitter.Duration
		}
		rules[i] = rule
	}

	return &Config{
		ListenAddr:    listen,
		Upstream:      strings.TrimRight(pc.Upstream, "/"),
		ProviderName:  name,
		OverloadRules: rules,
		Logging:       resolveLogging(lc),
	}, nil
}

func resolveLogging(lc loggingYAML) LoggingConfig {
	cfg := LoggingConfig{
		DatabasePath: "./logs.db",
		MaxAgeDays:   7,
	}
	if lc.Enabled != nil {
		cfg.Enabled = *lc.Enabled
	}
	if lc.DatabasePath != "" {
		cfg.DatabasePath = lc.DatabasePath
	}
	if lc.MaxAgeDays != nil {
		cfg.MaxAgeDays = *lc.MaxAgeDays
	}
	if v := os.Getenv("LOGGING_ENABLED"); v != "" {
		cfg.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("LOGGING_DATABASE_PATH"); v != "" {
		cfg.DatabasePath = v
	}
	return cfg
}
