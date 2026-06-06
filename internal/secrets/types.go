package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// SecretRef descreve como resolver um valor sensível a partir de uma fonte externa.
type SecretRef struct {
	Source string `yaml:"source"` // "env", "aws_secrets_manager", "hashicorp_vault"

	// env
	Var string `yaml:"var"`

	// aws_secrets_manager
	SecretID string `yaml:"secret_id"`
	Region   string `yaml:"region"`

	// hashicorp_vault
	Path      string `yaml:"path"`
	VaultRole string `yaml:"vault_role"`

	// compartilhado (aws + vault)
	Field   string        `yaml:"field"`   // campo dentro do JSON do secret
	Version string        `yaml:"version"` // versão do secret ("latest" ou número)
	RefreshBeforeExpiry time.Duration `yaml:"refresh_before_expiry"`
}

// CacheKey gera uma chave determinística para uso no cache do Resolver.
func (r SecretRef) CacheKey() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s",
		r.Source, r.Var, r.SecretID, r.Path, r.Field,
	)))
	return hex.EncodeToString(h[:16])
}

// SecretValue é o valor resolvido de um SecretRef.
type SecretValue struct {
	Value     string
	ExpiresAt time.Time // zero = sem expiração conhecida
}

// Provider é a interface implementada por cada fonte de secret.
type Provider interface {
	Get(ctx context.Context, ref SecretRef) (SecretValue, error)
}
