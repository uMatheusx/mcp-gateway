package config

import (
	"context"
	"fmt"
	"time"

	"github.com/uMatheusx/mcp-gateway/internal/secrets"
	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to support YAML string parsing ("30s", "5m", "1h").
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(v *yaml.Node) error {
	if v.Value == "" {
		return nil
	}
	dur, err := time.ParseDuration(v.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", v.Value, err)
	}
	d.Duration = dur
	return nil
}

// SecretOrValue holds either a literal string (dev only) or a reference to an external secret.
type SecretOrValue struct {
	literal  string
	ref      *secrets.SecretRef
	resolver *secrets.Resolver
}

func (sv *SecretOrValue) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		sv.literal = value.Value
		return nil
	}
	var ref secrets.SecretRef
	if err := value.Decode(&ref); err != nil {
		return fmt.Errorf("invalid secret reference: %w", err)
	}
	sv.ref = &ref
	return nil
}

// Resolve returns the resolved secret value.
func (sv *SecretOrValue) Resolve(ctx context.Context) (string, error) {
	if sv == nil {
		return "", nil
	}
	if sv.ref != nil {
		if sv.resolver == nil {
			return "", fmt.Errorf("secret resolver not initialized")
		}
		return sv.resolver.Resolve(ctx, *sv.ref)
	}
	return sv.literal, nil
}

// Ref returns the underlying SecretRef; for literals returns a synthetic ref with source="literal".
func (sv *SecretOrValue) Ref() secrets.SecretRef {
	if sv == nil || sv.ref == nil {
		lit := ""
		if sv != nil {
			lit = sv.literal
		}
		return secrets.SecretRef{Source: "literal", Var: lit}
	}
	return *sv.ref
}

// IsLiteral reports whether this value is a plain string (not a secret reference).
func (sv *SecretOrValue) IsLiteral() bool {
	return sv != nil && sv.ref == nil && sv.literal != ""
}

// GatewayConfig is the root configuration structure.
type GatewayConfig struct {
	Name     string                `yaml:"name"`
	Version  string                `yaml:"version"`
	Security SecurityConfig        `yaml:"security"`
	APIs     map[string]APIConfig  `yaml:"apis"`
	Tools    map[string]ToolConfig `yaml:"tools"`
}

type SecurityConfig struct {
	APIKeys   []AgentKeyConfig `yaml:"api_keys"`
	RateLimit RateLimitConfig  `yaml:"rate_limiting"`
	DevMode   bool             `yaml:"dev_mode"`
}

type AgentKeyConfig struct {
	ID           string         `yaml:"id"`
	Key          *SecretOrValue `yaml:"key"`
	Name         string         `yaml:"name"`
	AllowedTools []string       `yaml:"allowed_tools"`
}

// AllowsTool reports whether this agent is permitted to call the given tool.
func (a AgentKeyConfig) AllowsTool(toolName string) bool {
	for _, t := range a.AllowedTools {
		if t == "*" || t == toolName {
			return true
		}
	}
	return false
}

type APIConfig struct {
	BaseURL string            `yaml:"base_url"`
	Auth    AuthConfig        `yaml:"auth"`
	Headers map[string]string `yaml:"headers"`
	Timeout Duration          `yaml:"timeout"`
	Retry   RetryConfig       `yaml:"retry"`
}

type AuthConfig struct {
	Type string `yaml:"type"`

	// oauth2_client_credentials
	TokenURL     string            `yaml:"token_url"`
	ClientID     *SecretOrValue    `yaml:"client_id"`
	ClientSecret *SecretOrValue    `yaml:"client_secret"`
	Scope        string            `yaml:"scope"`
	Audience     string            `yaml:"audience"`
	ExtraParams  map[string]string `yaml:"extra_params"`

	// oauth2_token_exchange
	ActorClientID     *SecretOrValue `yaml:"actor_client_id"`
	ActorClientSecret *SecretOrValue `yaml:"actor_client_secret"`

	// bearer
	Token *SecretOrValue `yaml:"token"`

	// api_key_header
	HeaderName string         `yaml:"header_name"`
	Key        *SecretOrValue `yaml:"key"`

	// api_key_query
	ParamName string `yaml:"param_name"`

	// basic
	Username *SecretOrValue `yaml:"username"`
	Password *SecretOrValue `yaml:"password"`

	// aws_sigv4
	Region  string `yaml:"region"`
	Service string `yaml:"service"`

	// mtls
	CertFile *SecretOrValue `yaml:"cert_file"`
	KeyFile  *SecretOrValue `yaml:"key_file"`
	CAFile   string         `yaml:"ca_file"`
}

type ToolConfig struct {
	API                 string           `yaml:"api"`
	Description         string           `yaml:"description"`
	Method              string           `yaml:"method"`
	Path                string           `yaml:"path"`
	Parameters          []ParameterDef   `yaml:"parameters"`
	Body                *BodyDef         `yaml:"body,omitempty"`
	AuthOverride        *AuthOverride    `yaml:"auth_override,omitempty"`
	RateLimiting        *RateLimitConfig `yaml:"rate_limiting,omitempty"`
	ResponseTransform   *TransformConfig `yaml:"response_transform,omitempty"`
	RequireConfirmation bool             `yaml:"require_confirmation"`
	ConfirmationMessage string           `yaml:"confirmation_message,omitempty"`
}

type AuthOverride struct {
	Scope string `yaml:"scope"`
}

type ParameterDef struct {
	Name        string      `yaml:"name"`
	In          string      `yaml:"in"` // path, query, header, body
	Type        string      `yaml:"type"`
	Required    bool        `yaml:"required"`
	Default     interface{} `yaml:"default,omitempty"`
	Description string      `yaml:"description"`
	Enum        []string    `yaml:"enum,omitempty"`
	Minimum     *float64    `yaml:"minimum,omitempty"`
	Maximum     *float64    `yaml:"maximum,omitempty"`
}

type BodyDef struct {
	Type       string         `yaml:"type"`
	Properties []ParameterDef `yaml:"properties"`
}

type RetryConfig struct {
	MaxAttempts  int      `yaml:"max_attempts"`
	Backoff      string   `yaml:"backoff"` // exponential, linear, fixed
	InitialDelay Duration `yaml:"initial_delay"`
	MaxDelay     Duration `yaml:"max_delay"`
}

type RateLimitConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	RequestsPerHour   int `yaml:"requests_per_hour"`
	Burst             int `yaml:"burst"`
}

type TransformConfig struct {
	IncludeFields []string `yaml:"include_fields"`
	ExcludeFields []string `yaml:"exclude_fields"`
}
