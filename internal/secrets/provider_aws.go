package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// AWSProvider resolve secrets a partir do AWS Secrets Manager.
// Usa IAM Role automaticamente quando rodando em EKS/ECS/EC2.
// Em desenvolvimento, usa o profile configurado em ~/.aws/credentials ou LocalStack.
type AWSProvider struct {
	client *secretsmanager.Client
}

// NewAWSProvider cria um AWSProvider com um client customizado.
// Útil em testes para apontar para LocalStack.
func NewAWSProvider(client *secretsmanager.Client) *AWSProvider {
	return &AWSProvider{client: client}
}

func (p *AWSProvider) Get(ctx context.Context, ref SecretRef) (SecretValue, error) {
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(ref.SecretID),
	}
	if ref.Version != "" && ref.Version != "latest" {
		input.VersionId = aws.String(ref.Version)
	}

	out, err := p.client.GetSecretValue(ctx, input)
	if err != nil {
		return SecretValue{}, fmt.Errorf("GetSecretValue(%s): %w", ref.SecretID, err)
	}

	raw := aws.ToString(out.SecretString)

	if ref.Field != "" {
		var data map[string]string
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			return SecretValue{}, fmt.Errorf("secret %q is not valid JSON: %w", ref.SecretID, err)
		}
		val, ok := data[ref.Field]
		if !ok {
			return SecretValue{}, fmt.Errorf("field %q not found in secret %q (available: %v)",
				ref.Field, ref.SecretID, slices.Collect(maps.Keys(data)))
		}
		raw = val
	}

	// AWS não expõe TTL diretamente — 12h é um intervalo seguro de renovação.
	return SecretValue{
		Value:     raw,
		ExpiresAt: time.Now().Add(12 * time.Hour),
	}, nil
}
