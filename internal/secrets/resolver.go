package secrets

import (
	"context"
	"fmt"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/jellydator/ttlcache/v3"
)

// Resolver resolve SecretRefs de múltiplas fontes com cache TTL em memória.
type Resolver struct {
	providers map[string]Provider
	cache     *ttlcache.Cache[string, SecretValue]
}

// NewTestResolver cria um Resolver com providers controlados pelo chamador.
// Destinado exclusivamente a testes — não usar em produção.
func NewTestResolver(providers map[string]Provider) *Resolver {
	cache := ttlcache.New[string, SecretValue](
		ttlcache.WithTTL[string, SecretValue](15 * time.Minute),
	)
	go cache.Start()
	return &Resolver{providers: providers, cache: cache}
}

// NewResolver inicializa o Resolver detectando os providers disponíveis no ambiente.
// Providers não disponíveis são ignorados com aviso — o gateway sobe mesmo assim.
func NewResolver(ctx context.Context) (*Resolver, error) {
	providers := map[string]Provider{
		"env": &EnvProvider{},
	}

	// AWS Secrets Manager — usa IAM Role automaticamente se disponível.
	// Em dev, usa AWS_PROFILE ou credenciais do LocalStack.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err == nil {
		providers["aws_secrets_manager"] = &AWSProvider{
			client: secretsmanager.NewFromConfig(awsCfg),
		}
	}

	// HashiCorp Vault — só inicializa se VAULT_ADDR estiver definido.
	if addr := os.Getenv("VAULT_ADDR"); addr != "" {
		if vc, err := newVaultClient(ctx, addr); err == nil {
			providers["hashicorp_vault"] = &VaultProvider{client: vc}
		} else {
			fmt.Fprintf(os.Stderr, "warn: vault provider unavailable: %v\n", err)
		}
	}

	cache := ttlcache.New[string, SecretValue](
		ttlcache.WithTTL[string, SecretValue](15 * time.Minute),
	)
	go cache.Start()

	return &Resolver{
		providers: providers,
		cache:     cache,
	}, nil
}

// Resolve retorna o valor do secret, usando cache quando disponível.
// Inicia prefetch assíncrono quando o secret está próximo de expirar.
func (r *Resolver) Resolve(ctx context.Context, ref SecretRef) (string, error) {
	key := ref.CacheKey()

	if item := r.cache.Get(key); item != nil {
		sv := item.Value()
		if ref.RefreshBeforeExpiry > 0 && !sv.ExpiresAt.IsZero() {
			if time.Until(sv.ExpiresAt) <= ref.RefreshBeforeExpiry {
				go r.prefetch(context.Background(), ref, key)
			}
		}
		return sv.Value, nil
	}

	return r.fetch(ctx, ref, key)
}

func (r *Resolver) fetch(ctx context.Context, ref SecretRef, key string) (string, error) {
	provider, ok := r.providers[ref.Source]
	if !ok {
		return "", fmt.Errorf("secret source %q not configured (available: %v)", ref.Source, r.availableSources())
	}

	sv, err := provider.Get(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("[source=%s field=%s]: %w", ref.Source, ref.Field, err)
	}

	ttl := 15 * time.Minute
	if !sv.ExpiresAt.IsZero() {
		remaining := time.Until(sv.ExpiresAt)
		if ref.RefreshBeforeExpiry > 0 {
			remaining -= ref.RefreshBeforeExpiry
		}
		if remaining > 0 {
			ttl = remaining
		}
	}

	r.cache.Set(key, sv, ttl)
	return sv.Value, nil
}

func (r *Resolver) prefetch(ctx context.Context, ref SecretRef, key string) {
	provider, ok := r.providers[ref.Source]
	if !ok {
		return
	}
	sv, err := provider.Get(ctx, ref)
	if err != nil {
		return // silencioso — próxima chamada tenta novamente
	}
	ttl := 15 * time.Minute
	if !sv.ExpiresAt.IsZero() {
		if remaining := time.Until(sv.ExpiresAt); remaining > 0 {
			ttl = remaining
		}
	}
	r.cache.Set(key, sv, ttl)
}

func (r *Resolver) availableSources() []string {
	sources := make([]string, 0, len(r.providers))
	for s := range r.providers {
		sources = append(sources, s)
	}
	return sources
}
