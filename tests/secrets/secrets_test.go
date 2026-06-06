package secrets_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	vault "github.com/hashicorp/vault-client-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uMatheusx/mcp-gateway/internal/secrets"
)

// ─── mock provider ───────────────────────────────────────────────────────────

type mockProvider struct {
	mu    sync.Mutex
	value string
	calls int
}

func (m *mockProvider) Get(_ context.Context, _ secrets.SecretRef) (secrets.SecretValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.value == "" {
		return secrets.SecretValue{}, fmt.Errorf("mock: no value configured")
	}
	return secrets.SecretValue{Value: m.value}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func newMockAWSClient(t *testing.T, handler http.HandlerFunc) *secretsmanager.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return secretsmanager.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
	}, func(o *secretsmanager.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
	})
}

func newMockVaultClient(t *testing.T, handler http.HandlerFunc) *vault.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := vault.New(vault.WithAddress(srv.URL))
	require.NoError(t, err)
	client.SetToken("test-token")
	return client
}

// ─── EnvProvider ─────────────────────────────────────────────────────────────

func TestEnvProvider_Get_ExistingVar(t *testing.T) {
	t.Setenv("MY_SECRET_TEST", "valor-do-env")
	p := &secrets.EnvProvider{}
	sv, err := p.Get(context.Background(), secrets.SecretRef{Source: "env", Var: "MY_SECRET_TEST"})
	require.NoError(t, err)
	assert.Equal(t, "valor-do-env", sv.Value)
	assert.True(t, sv.ExpiresAt.IsZero(), "variáveis de ambiente não têm expiração")
}

func TestEnvProvider_Get_MissingVar(t *testing.T) {
	p := &secrets.EnvProvider{}
	_, err := p.Get(context.Background(), secrets.SecretRef{Source: "env", Var: "ESTA_VAR_NAO_EXISTE_XYZ123"})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "ESTA_VAR_NAO_EXISTE_XYZ123")
}

// ─── AWSProvider ─────────────────────────────────────────────────────────────

func TestAWSProvider_Get_SimpleString(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ARN":          "arn:aws:secretsmanager:us-east-1:123:secret:my-secret",
			"Name":         "my-secret",
			"SecretString": "plain-value",
		})
	}
	p := secrets.NewAWSProvider(newMockAWSClient(t, handler))
	sv, err := p.Get(context.Background(), secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "my-secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "plain-value", sv.Value)
	assert.False(t, sv.ExpiresAt.IsZero(), "AWS secrets devem ter expiração de 12h")
}

func TestAWSProvider_Get_JSONField(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ARN":          "arn:aws:secretsmanager:us-east-1:123:secret:crm-creds",
			"Name":         "crm-creds",
			"SecretString": `{"client_id":"test-id","client_secret":"test-secret"}`,
		})
	}
	p := secrets.NewAWSProvider(newMockAWSClient(t, handler))
	sv, err := p.Get(context.Background(), secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "crm-creds",
		Field:    "client_secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "test-secret", sv.Value)
}

func TestAWSProvider_Get_FieldNotFound(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"SecretString": `{"client_id":"test-id"}`,
		})
	}
	p := secrets.NewAWSProvider(newMockAWSClient(t, handler))
	_, err := p.Get(context.Background(), secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "crm-creds",
		Field:    "campo_inexistente",
	})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "campo_inexistente")
}

func TestAWSProvider_Get_SecretNotFound(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"__type":  "ResourceNotFoundException",
			"message": "Secrets Manager can't find the specified secret.",
		})
	}
	p := secrets.NewAWSProvider(newMockAWSClient(t, handler))
	_, err := p.Get(context.Background(), secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "secret/que/nao/existe",
	})
	assert.Error(t, err)
}

func TestAWSProvider_Get_InvalidJSON(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"SecretString": "nao-eh-json",
		})
	}
	p := secrets.NewAWSProvider(newMockAWSClient(t, handler))
	_, err := p.Get(context.Background(), secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "my-secret",
		Field:    "algum_campo",
	})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "not valid JSON")
}

// ─── VaultProvider ───────────────────────────────────────────────────────────

func TestVaultProvider_Get_InvalidPathFormat(t *testing.T) {
	// Path sem /data/ é rejeitado antes de qualquer chamada HTTP
	client, err := vault.New(vault.WithAddress("http://localhost:9999"))
	require.NoError(t, err)
	p := secrets.NewVaultProvider(client)
	_, err = p.Get(context.Background(), secrets.SecretRef{
		Source: "hashicorp_vault",
		Path:   "secret/sem-separador-data",
		Field:  "key",
	})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "KV v2 format")
}

func TestVaultProvider_Get_Field(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"request_id":     "test-id",
			"lease_id":       "",
			"renewable":      false,
			"lease_duration": 0,
			"data": map[string]interface{}{
				"data": map[string]interface{}{
					"client_id":     "vault-client-id",
					"client_secret": "vault-secret",
				},
				"metadata": map[string]interface{}{
					"version":      1,
					"created_time": "2024-01-01T00:00:00.000000000Z",
				},
			},
			"warnings": nil,
			"auth":     nil,
		})
	}
	p := secrets.NewVaultProvider(newMockVaultClient(t, handler))
	sv, err := p.Get(context.Background(), secrets.SecretRef{
		Source: "hashicorp_vault",
		Path:   "secret/data/gateway/erp",
		Field:  "client_id",
	})
	require.NoError(t, err)
	assert.Equal(t, "vault-client-id", sv.Value)
}

func TestVaultProvider_Get_FieldNotFound(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"data": map[string]interface{}{
					"client_id": "vault-client-id",
				},
				"metadata": map[string]interface{}{"version": 1},
			},
		})
	}
	p := secrets.NewVaultProvider(newMockVaultClient(t, handler))
	_, err := p.Get(context.Background(), secrets.SecretRef{
		Source: "hashicorp_vault",
		Path:   "secret/data/gateway/erp",
		Field:  "campo_inexistente",
	})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "campo_inexistente")
}

// ─── Resolver ────────────────────────────────────────────────────────────────

func TestResolver_Resolve_EnvSource(t *testing.T) {
	t.Setenv("RESOLVER_TEST_VAR", "test-value")
	r := secrets.NewTestResolver(map[string]secrets.Provider{
		"env": &secrets.EnvProvider{},
	})
	val, err := r.Resolve(context.Background(), secrets.SecretRef{
		Source: "env",
		Var:    "RESOLVER_TEST_VAR",
	})
	require.NoError(t, err)
	assert.Equal(t, "test-value", val)
}

func TestResolver_Resolve_CacheHit(t *testing.T) {
	mock := &mockProvider{value: "cached-value"}
	r := secrets.NewTestResolver(map[string]secrets.Provider{
		"mock": mock,
	})
	ref := secrets.SecretRef{Source: "mock", Var: "qualquer"}

	val1, err := r.Resolve(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, "cached-value", val1)

	val2, err := r.Resolve(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, "cached-value", val2)

	mock.mu.Lock()
	calls := mock.calls
	mock.mu.Unlock()
	assert.Equal(t, 1, calls, "provider deve ser chamado uma única vez — segunda chamada deve vir do cache")
}

func TestResolver_Resolve_SourceNotConfigured(t *testing.T) {
	r := secrets.NewTestResolver(map[string]secrets.Provider{
		"env": &secrets.EnvProvider{},
	})
	_, err := r.Resolve(context.Background(), secrets.SecretRef{
		Source: "azure_keyvault",
		Field:  "qualquer",
	})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "azure_keyvault")
}

// ─── SecretRef ───────────────────────────────────────────────────────────────

func TestSecretRef_CacheKey_Deterministic(t *testing.T) {
	ref := secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "prod/crm/credentials",
		Field:    "client_secret",
	}
	assert.Equal(t, ref.CacheKey(), ref.CacheKey(), "mesma ref deve sempre gerar a mesma chave")
	assert.NotEmpty(t, ref.CacheKey())
}

func TestSecretRef_CacheKey_UniquePerRef(t *testing.T) {
	ref1 := secrets.SecretRef{Source: "env", Var: "FOO"}
	ref2 := secrets.SecretRef{Source: "env", Var: "BAR"}
	assert.NotEqual(t, ref1.CacheKey(), ref2.CacheKey(), "refs diferentes devem gerar chaves diferentes")
}
