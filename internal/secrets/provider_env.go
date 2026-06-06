package secrets

import (
	"context"
	"fmt"
	"os"
)

// EnvProvider resolve secrets a partir de variáveis de ambiente.
type EnvProvider struct{}

func (p *EnvProvider) Get(_ context.Context, ref SecretRef) (SecretValue, error) {
	val := os.Getenv(ref.Var)
	if val == "" {
		return SecretValue{}, fmt.Errorf("environment variable %q is not set or empty", ref.Var)
	}
	// Variáveis de ambiente não expiram — ExpiresAt zero indica isso ao Resolver.
	return SecretValue{Value: val}, nil
}
