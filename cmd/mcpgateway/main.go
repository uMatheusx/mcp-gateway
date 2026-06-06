package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/uMatheusx/mcp-gateway/internal/secrets"
)

func main() {
	ctx := context.Background()

	resolver, err := secrets.NewResolver(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "erro ao inicializar resolver: %v\n", err)
		os.Exit(1)
	}

	testEnv(ctx, resolver)
	testVault(ctx, resolver)
	testAWS(ctx, resolver)
	testCache(ctx, resolver)
	testSourceNaoConfigurada(ctx, resolver)
}

func testEnv(ctx context.Context, r *secrets.Resolver) {
	fmt.Println("\n=== ENV ===")

	os.Setenv("MEU_SECRET_TESTE", "valor-do-env")

	val, err := r.Resolve(ctx, secrets.SecretRef{Source: "env", Var: "MEU_SECRET_TESTE"})
	printResult("variável existente", val, err)

	_, err = r.Resolve(ctx, secrets.SecretRef{Source: "env", Var: "VAR_INEXISTENTE"})
	printResult("variável inexistente (erro esperado)", "", err)
}

func testVault(ctx context.Context, r *secrets.Resolver) {
	fmt.Println("\n=== VAULT ===")

	if os.Getenv("VAULT_ADDR") == "" {
		fmt.Println("PULADO — VAULT_ADDR não definido")
		return
	}

	val, err := r.Resolve(ctx, secrets.SecretRef{
		Source: "hashicorp_vault",
		Path:   "secret/data/dev/test",
		Field:  "client_id",
	})
	printResult("client_id", val, err)

	val, err = r.Resolve(ctx, secrets.SecretRef{
		Source: "hashicorp_vault",
		Path:   "secret/data/dev/test",
		Field:  "client_secret",
	})
	printResult("client_secret", val, err)

	// Erro: field que não existe
	_, err = r.Resolve(ctx, secrets.SecretRef{
		Source: "hashicorp_vault",
		Path:   "secret/data/dev/test",
		Field:  "campo_inexistente",
	})
	printResult("field inexistente (erro esperado)", "", err)

	// Erro: path com formato errado (sem /data/)
	_, err = r.Resolve(ctx, secrets.SecretRef{
		Source: "hashicorp_vault",
		Path:   "secret/dev/test",
		Field:  "client_id",
	})
	printResult("path inválido (erro esperado)", "", err)
}

func testAWS(ctx context.Context, r *secrets.Resolver) {
	fmt.Println("\n=== AWS SECRETS MANAGER ===")

	if os.Getenv("AWS_ENDPOINT_URL") == "" && os.Getenv("AWS_PROFILE") == "" {
		fmt.Println("PULADO — AWS_ENDPOINT_URL ou AWS_PROFILE não definido")
		return
	}

	val, err := r.Resolve(ctx, secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "dev/crm/credentials",
		Field:    "client_id",
	})
	printResult("client_id", val, err)

	val, err = r.Resolve(ctx, secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "dev/crm/credentials",
		Field:    "client_secret",
	})
	printResult("client_secret", val, err)

	// Erro: field que não existe no JSON
	_, err = r.Resolve(ctx, secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "dev/crm/credentials",
		Field:    "campo_inexistente",
	})
	printResult("field inexistente (erro esperado)", "", err)

	// Erro: secret que não existe
	_, err = r.Resolve(ctx, secrets.SecretRef{
		Source:   "aws_secrets_manager",
		SecretID: "secret/que/nao/existe",
	})
	printResult("secret inexistente (erro esperado)", "", err)
}

func testCache(ctx context.Context, r *secrets.Resolver) {
	fmt.Println("\n=== CACHE ===")

	os.Setenv("CACHE_TEST", "valor-cacheado")
	ref := secrets.SecretRef{Source: "env", Var: "CACHE_TEST"}

	// Primeira chamada — vai ao provider
	t1 := time.Now()
	val, _ := r.Resolve(ctx, ref)
	d1 := time.Since(t1)
	fmt.Printf("1ª chamada: %q (%v)\n", val, d1)

	// Segunda chamada — deve vir do cache (mais rápida)
	t2 := time.Now()
	val, _ = r.Resolve(ctx, ref)
	d2 := time.Since(t2)
	fmt.Printf("2ª chamada: %q (%v) ← deve ser mais rápida\n", val, d2)
}

func testSourceNaoConfigurada(ctx context.Context, r *secrets.Resolver) {
	fmt.Println("\n=== SOURCE NÃO CONFIGURADA ===")

	_, err := r.Resolve(ctx, secrets.SecretRef{
		Source: "azure_keyvault",
		Field:  "qualquer",
	})
	printResult("azure não configurado (erro esperado)", "", err)
}

func printResult(label, val string, err error) {
	if err != nil {
		fmt.Printf("%-40s → ERRO: %v\n", label, err)
	} else {
		fmt.Printf("%-40s → OK: %q\n", label, val)
	}
}
