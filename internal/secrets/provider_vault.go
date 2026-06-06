package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	vault "github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"
)

// VaultProvider resolve secrets a partir do HashiCorp Vault (KV v2).
type VaultProvider struct {
	client *vault.Client
}

// NewVaultProvider cria um VaultProvider com um client customizado.
// Útil em testes para apontar para um Vault local.
func NewVaultProvider(client *vault.Client) *VaultProvider {
	return &VaultProvider{client: client}
}

func (p *VaultProvider) Get(ctx context.Context, ref SecretRef) (SecretValue, error) {
	// path KV v2 completo: "mount/data/key-path"
	// Ex: "secret/data/gateway/erp" → mount="secret", keyPath="gateway/erp"
	parts := strings.SplitN(ref.Path, "/data/", 2)
	if len(parts) != 2 {
		return SecretValue{}, fmt.Errorf("vault path must be KV v2 format (mount/data/key-path), got %q", ref.Path)
	}
	mount := parts[0]
	keyPath := parts[1]

	secret, err := p.client.Secrets.KvV2Read(ctx, keyPath,
		vault.WithMountPath(mount),
	)
	if err != nil {
		return SecretValue{}, fmt.Errorf("vault KV read(%s): %w", ref.Path, err)
	}

	data := secret.Data.Data

	val, ok := data[ref.Field]
	if !ok {
		return SecretValue{}, fmt.Errorf("field %q not found at vault path %q", ref.Field, ref.Path)
	}
	strVal, ok := val.(string)
	if !ok {
		return SecretValue{}, fmt.Errorf("field %q at vault path %q is not a string", ref.Field, ref.Path)
	}

	leaseDuration := time.Duration(secret.LeaseDuration) * time.Second
	var expiresAt time.Time
	if leaseDuration > 0 {
		expiresAt = time.Now().Add(leaseDuration)
	}

	return SecretValue{Value: strVal, ExpiresAt: expiresAt}, nil
}

// newVaultClient inicializa o client Vault detectando o método de auth disponível.
func newVaultClient(ctx context.Context, addr string) (*vault.Client, error) {
	client, err := vault.New(
		vault.WithAddress(addr),
	)
	if err != nil {
		return nil, err
	}

	// AppRole — produção em VM sem identity platform
	roleID := os.Getenv("VAULT_ROLE_ID")
	secretID := os.Getenv("VAULT_SECRET_ID")
	if roleID != "" && secretID != "" {
		resp, err := client.Auth.AppRoleLogin(ctx, schema.AppRoleLoginRequest{
			RoleId:   roleID,
			SecretId: secretID,
		})
		if err == nil {
			if resp.Auth == nil {
				return nil, fmt.Errorf("vault AppRole login returned no auth token")
			}
			client.SetToken(resp.Auth.ClientToken)
			return client, nil
		}
	}

	// Token direto — desenvolvimento local (VAULT_TOKEN=dev-root-token)
	if token := os.Getenv("VAULT_TOKEN"); token != "" {
		client.SetToken(token)
		return client, nil
	}

	return nil, fmt.Errorf("no vault auth method available — set VAULT_ROLE_ID+VAULT_SECRET_ID or VAULT_TOKEN")
}
