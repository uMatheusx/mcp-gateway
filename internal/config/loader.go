package config

import (
	"context"
	"fmt"
	"os"

	"github.com/uMatheusx/mcp-gateway/internal/secrets"
	"gopkg.in/yaml.v3"
)

// Load reads a YAML config file, validates it, and injects the secret resolver.
func Load(_ context.Context, path string, resolver *secrets.Resolver) (*GatewayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	return loadFromBytes(data, resolver)
}

// LoadFromString parses and validates a YAML config string without injecting a resolver.
// Intended for validation and testing.
func LoadFromString(data string) (*GatewayConfig, error) {
	return loadFromBytes([]byte(data), nil)
}

func loadFromBytes(data []byte, resolver *secrets.Resolver) (*GatewayConfig, error) {
	var cfg GatewayConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing yaml: %w", err)
	}
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	if resolver != nil {
		injectResolver(&cfg, resolver)
	}
	return &cfg, nil
}

// injectResolver walks all SecretOrValue fields and sets their resolver so they can resolve lazily.
func injectResolver(cfg *GatewayConfig, r *secrets.Resolver) {
	for i := range cfg.Security.APIKeys {
		injectSOV(cfg.Security.APIKeys[i].Key, r)
	}
	for name, api := range cfg.APIs {
		injectSOV(api.Auth.ClientID, r)
		injectSOV(api.Auth.ClientSecret, r)
		injectSOV(api.Auth.ActorClientID, r)
		injectSOV(api.Auth.ActorClientSecret, r)
		injectSOV(api.Auth.Token, r)
		injectSOV(api.Auth.Key, r)
		injectSOV(api.Auth.Username, r)
		injectSOV(api.Auth.Password, r)
		injectSOV(api.Auth.CertFile, r)
		injectSOV(api.Auth.KeyFile, r)
		cfg.APIs[name] = api
	}
}

func injectSOV(sov *SecretOrValue, r *secrets.Resolver) {
	if sov != nil {
		sov.resolver = r
	}
}
