package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uMatheusx/mcp-gateway/internal/config"
)

const validYAML = `
name: "test-gateway"
version: "1.0"

apis:
  jsonplaceholder:
    base_url: "https://jsonplaceholder.typicode.com"
    auth:
      type: none
    timeout: 10s
    retry:
      max_attempts: 3
      backoff: exponential
      initial_delay: 500ms
      max_delay: 10s

tools:
  get_user:
    api: jsonplaceholder
    description: "Get a user by ID"
    method: GET
    path: "/users/{user_id}"
    parameters:
      - name: user_id
        in: path
        type: integer
        required: true
        description: "User ID (1-10)"
  list_posts:
    api: jsonplaceholder
    description: "List all posts"
    method: GET
    path: "/posts"
    parameters:
      - name: userId
        in: query
        type: integer
        required: false
`

func TestLoadFromString_ValidConfig(t *testing.T) {
	cfg, err := config.LoadFromString(validYAML)
	require.NoError(t, err)
	assert.Equal(t, "test-gateway", cfg.Name)
	assert.Equal(t, "1.0", cfg.Version)
	assert.Contains(t, cfg.APIs, "jsonplaceholder")
	assert.Contains(t, cfg.Tools, "get_user")
	assert.Contains(t, cfg.Tools, "list_posts")
	assert.Equal(t, "none", cfg.APIs["jsonplaceholder"].Auth.Type)
	assert.Equal(t, "https://jsonplaceholder.typicode.com", cfg.APIs["jsonplaceholder"].BaseURL)
}

func TestLoadFromString_DurationParsing(t *testing.T) {
	cfg, err := config.LoadFromString(validYAML)
	require.NoError(t, err)
	api := cfg.APIs["jsonplaceholder"]
	assert.Equal(t, 10*time.Second, api.Timeout.Duration)
	assert.Equal(t, 3, api.Retry.MaxAttempts)
	assert.Equal(t, "exponential", api.Retry.Backoff)
	assert.Equal(t, 500*time.Millisecond, api.Retry.InitialDelay.Duration)
	assert.Equal(t, 10*time.Second, api.Retry.MaxDelay.Duration)
}

func TestLoadFromString_ToolParams(t *testing.T) {
	cfg, err := config.LoadFromString(validYAML)
	require.NoError(t, err)
	tool := cfg.Tools["get_user"]
	require.Len(t, tool.Parameters, 1)
	assert.Equal(t, "user_id", tool.Parameters[0].Name)
	assert.Equal(t, "path", tool.Parameters[0].In)
	assert.Equal(t, "integer", tool.Parameters[0].Type)
	assert.True(t, tool.Parameters[0].Required)
}

func TestValidatorRejectsLiteralClientSecret(t *testing.T) {
	yaml := `
apis:
  crm:
    base_url: "https://api.example.com"
    auth:
      type: oauth2_client_credentials
      token_url: "https://sts.example.com/token"
      client_id:
        source: env
        var: CRM_CLIENT_ID
      client_secret: "senha-em-texto-claro"
tools:
  get_customer:
    api: crm
    description: "Get a customer"
    method: GET
    path: "/customers/{id}"
    parameters:
      - name: id
        in: path
        type: string
        required: true
`
	_, err := config.LoadFromString(yaml)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "client_secret cannot be a literal value")
}

func TestValidatorRejectsLiteralToken(t *testing.T) {
	yaml := `
apis:
  myapi:
    base_url: "https://api.example.com"
    auth:
      type: bearer
      token: "hardcoded-token-value"
tools: {}
`
	_, err := config.LoadFromString(yaml)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "token cannot be a literal value")
}

func TestValidatorRejectsLiteralAgentKey(t *testing.T) {
	yaml := `
security:
  api_keys:
    - id: "my-agent"
      key: "hardcoded-key"
      name: "My Agent"
      allowed_tools: ["*"]
apis:
  myapi:
    base_url: "https://api.example.com"
    auth:
      type: none
tools: {}
`
	_, err := config.LoadFromString(yaml)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "key cannot be a literal value")
}

func TestValidatorRejectsUnknownAPI(t *testing.T) {
	yaml := `
apis:
  myapi:
    base_url: "https://api.example.com"
    auth:
      type: none
tools:
  my_tool:
    api: nonexistent
    description: "A tool"
    method: GET
    path: "/path"
`
	_, err := config.LoadFromString(yaml)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "references unknown api")
}

func TestValidatorRejectsPathParamNotInPath(t *testing.T) {
	yaml := `
apis:
  myapi:
    base_url: "https://api.example.com"
    auth:
      type: none
tools:
  my_tool:
    api: myapi
    description: "A tool"
    method: GET
    path: "/users"
    parameters:
      - name: user_id
        in: path
        type: string
        required: true
`
	_, err := config.LoadFromString(yaml)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "path param")
	assert.ErrorContains(t, err, "not found in path")
}

func TestValidatorRejectsConfirmationWithoutMessage(t *testing.T) {
	yaml := `
apis:
  myapi:
    base_url: "https://api.example.com"
    auth:
      type: none
tools:
  my_tool:
    api: myapi
    description: "A tool"
    method: POST
    path: "/action"
    require_confirmation: true
`
	_, err := config.LoadFromString(yaml)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "require_confirmation=true but confirmation_message is empty")
}

func TestValidatorRejectsAuthOverrideScopeOnNonOAuth(t *testing.T) {
	yaml := `
apis:
  myapi:
    base_url: "https://api.example.com"
    auth:
      type: none
tools:
  my_tool:
    api: myapi
    description: "A tool"
    method: GET
    path: "/path"
    auth_override:
      scope: "read"
`
	_, err := config.LoadFromString(yaml)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "auth_override.scope only works with oauth2 auth types")
}

func TestAllowsTool(t *testing.T) {
	agent := config.AgentKeyConfig{
		ID:           "test",
		AllowedTools: []string{"get_user", "list_posts"},
	}
	assert.True(t, agent.AllowsTool("get_user"))
	assert.True(t, agent.AllowsTool("list_posts"))
	assert.False(t, agent.AllowsTool("delete_user"))
}

func TestAllowsTool_Wildcard(t *testing.T) {
	agent := config.AgentKeyConfig{
		ID:           "test",
		AllowedTools: []string{"*"},
	}
	assert.True(t, agent.AllowsTool("any_tool"))
	assert.True(t, agent.AllowsTool("another_tool"))
}

func TestInvalidYAML(t *testing.T) {
	_, err := config.LoadFromString("this: is: not: valid: yaml: :")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "parsing yaml")
}

func TestInvalidDuration(t *testing.T) {
	yaml := `
apis:
  myapi:
    base_url: "https://api.example.com"
    auth:
      type: none
    timeout: "notaduration"
tools: {}
`
	_, err := config.LoadFromString(yaml)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "invalid duration")
}
