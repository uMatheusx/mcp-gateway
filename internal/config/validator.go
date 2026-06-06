package config

import (
	"fmt"
	"os"
	"strings"
)

var oauthTypes = map[string]bool{
	"oauth2_client_credentials": true,
	"oauth2_token_exchange":     true,
}

func validate(cfg *GatewayConfig) error {
	var errs []string

	if cfg.Security.DevMode {
		fmt.Fprintln(os.Stderr, "WARNING: security.dev_mode=true disables agent authentication — never use in production")
	}

	for toolName, tool := range cfg.Tools {
		api, ok := cfg.APIs[tool.API]
		if !ok {
			errs = append(errs, fmt.Sprintf("tool %q: references unknown api %q", toolName, tool.API))
			continue
		}

		for _, p := range tool.Parameters {
			if p.In == "path" && !strings.Contains(tool.Path, "{"+p.Name+"}") {
				errs = append(errs, fmt.Sprintf(
					"tool %q: path param %q not found in path %q", toolName, p.Name, tool.Path))
			}
		}

		if tool.RequireConfirmation && tool.ConfirmationMessage == "" {
			errs = append(errs, fmt.Sprintf(
				"tool %q: require_confirmation=true but confirmation_message is empty", toolName))
		}

		if tool.AuthOverride != nil && tool.AuthOverride.Scope != "" && !oauthTypes[api.Auth.Type] {
			errs = append(errs, fmt.Sprintf(
				"tool %q: auth_override.scope only works with oauth2 auth types, api %q uses %q",
				toolName, tool.API, api.Auth.Type))
		}

		if api.Auth.Type == "oauth2_token_exchange" {
			fmt.Fprintf(os.Stderr, "INFO: tool %q uses oauth2_token_exchange — this will fail in stdio mode (no subject_token available)\n", toolName)
		}
	}

	for apiName, api := range cfg.APIs {
		fields := []struct {
			name string
			sov  *SecretOrValue
		}{
			{"client_id", api.Auth.ClientID},
			{"client_secret", api.Auth.ClientSecret},
			{"actor_client_id", api.Auth.ActorClientID},
			{"actor_client_secret", api.Auth.ActorClientSecret},
			{"token", api.Auth.Token},
			{"key", api.Auth.Key},
			{"username", api.Auth.Username},
			{"password", api.Auth.Password},
			{"cert_file", api.Auth.CertFile},
			{"key_file", api.Auth.KeyFile},
		}
		for _, f := range fields {
			if f.sov.IsLiteral() {
				errs = append(errs, fmt.Sprintf(
					"api %s: %s cannot be a literal value — use a secret reference (source: env/vault/aws/azure/gcp)",
					apiName, f.name))
			}
		}
	}

	for i, key := range cfg.Security.APIKeys {
		if key.Key.IsLiteral() {
			errs = append(errs, fmt.Sprintf(
				"security.api_keys[%d] (%s): key cannot be a literal value", i, key.ID))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
