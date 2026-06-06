package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/uMatheusx/mcp-gateway/internal/config"
	"github.com/uMatheusx/mcp-gateway/internal/retry"
	"github.com/uMatheusx/mcp-gateway/internal/transform"
)

// Handler executes tool calls by translating them into HTTP requests.
type Handler struct {
	cfg    *config.GatewayConfig
	client *http.Client
}

// New creates a Handler backed by cfg.
func New(cfg *config.GatewayConfig) *Handler {
	return &Handler{
		cfg:    cfg,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// CallTool executes the named tool with args and returns the response body as a string.
func (h *Handler) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	tool, ok := h.cfg.Tools[toolName]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", toolName)
	}

	api, ok := h.cfg.APIs[tool.API]
	if !ok {
		return "", fmt.Errorf("unknown api %q", tool.API)
	}

	req, err := h.buildRequest(ctx, api, tool, args)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}

	if err := h.applyAuth(ctx, req, api.Auth); err != nil {
		return "", fmt.Errorf("applying auth: %w", err)
	}

	for k, v := range api.Headers {
		req.Header.Set(k, v)
	}

	retryCfg := retry.Config{
		MaxAttempts:  api.Retry.MaxAttempts,
		Backoff:      api.Retry.Backoff,
		InitialDelay: api.Retry.InitialDelay.Duration,
		MaxDelay:     api.Retry.MaxDelay.Duration,
	}

	resp, err := retry.Execute(ctx, h.client, req, retryCfg)
	if err != nil {
		return "", fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("api returned %d: %s", resp.StatusCode, string(body))
	}

	if tool.ResponseTransform != nil {
		body, err = transform.Apply(body, tool.ResponseTransform.IncludeFields, tool.ResponseTransform.ExcludeFields)
		if err != nil {
			return "", fmt.Errorf("transforming response: %w", err)
		}
	}

	return string(body), nil
}

func (h *Handler) buildRequest(ctx context.Context, api config.APIConfig, tool config.ToolConfig, args map[string]interface{}) (*http.Request, error) {
	path := tool.Path
	queryParams := url.Values{}
	bodyFields := make(map[string]interface{})

	for _, param := range tool.Parameters {
		val, provided := args[param.Name]
		if !provided {
			if param.Required {
				return nil, fmt.Errorf("missing required parameter %q", param.Name)
			}
			if param.Default != nil {
				val = param.Default
			} else {
				continue
			}
		}

		strVal := valueToString(val)
		switch param.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+param.Name+"}", url.PathEscape(strVal))
		case "query":
			queryParams.Set(param.Name, strVal)
		case "header":
			// applied after request creation in the loop below
		case "body":
			bodyFields[param.Name] = val
		}
	}

	rawURL := strings.TrimRight(api.BaseURL, "/") + path
	if len(queryParams) > 0 {
		rawURL += "?" + queryParams.Encode()
	}

	var bodyReader io.Reader
	if tool.Body != nil {
		bodyData := make(map[string]interface{})
		for _, prop := range tool.Body.Properties {
			if v, ok := args[prop.Name]; ok {
				bodyData[prop.Name] = v
			}
		}
		data, err := json.Marshal(bodyData)
		if err != nil {
			return nil, fmt.Errorf("encoding body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	} else if len(bodyFields) > 0 {
		data, err := json.Marshal(bodyFields)
		if err != nil {
			return nil, fmt.Errorf("encoding body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, tool.Method, rawURL, bodyReader)
	if err != nil {
		return nil, err
	}

	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	// Apply header params
	for _, param := range tool.Parameters {
		if param.In == "header" {
			if v, ok := args[param.Name]; ok {
				req.Header.Set(param.Name, valueToString(v))
			}
		}
	}

	return req, nil
}

func (h *Handler) applyAuth(ctx context.Context, req *http.Request, auth config.AuthConfig) error {
	switch auth.Type {
	case "none", "":
		return nil

	case "bearer":
		token, err := auth.Token.Resolve(ctx)
		if err != nil {
			return fmt.Errorf("resolving bearer token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

	case "api_key_header":
		key, err := auth.Key.Resolve(ctx)
		if err != nil {
			return fmt.Errorf("resolving api key: %w", err)
		}
		name := auth.HeaderName
		if name == "" {
			name = "X-API-Key"
		}
		req.Header.Set(name, key)

	case "api_key_query":
		key, err := auth.Key.Resolve(ctx)
		if err != nil {
			return fmt.Errorf("resolving api key: %w", err)
		}
		q := req.URL.Query()
		q.Set(auth.ParamName, key)
		req.URL.RawQuery = q.Encode()

	case "basic":
		username, err := auth.Username.Resolve(ctx)
		if err != nil {
			return fmt.Errorf("resolving basic username: %w", err)
		}
		password, err := auth.Password.Resolve(ctx)
		if err != nil {
			return fmt.Errorf("resolving basic password: %w", err)
		}
		req.SetBasicAuth(username, password)

	case "oauth2_client_credentials", "oauth2_token_exchange", "aws_sigv4", "mtls":
		return fmt.Errorf("auth type %q requires the auth manager (not yet implemented)", auth.Type)

	default:
		return fmt.Errorf("unknown auth type %q", auth.Type)
	}
	return nil
}

// valueToString converts an argument value to its string representation.
func valueToString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
