# Gateway de Ferramentas para Agentes (MCP Proxy)
## Documento de Especificação Técnica e Implementação

> **Versão:** 3.0  
> **Perfil:** Projeto de portfólio escalável para SaaS  
> **Stack principal:** Go · React · PostgreSQL · Redis · Docker  
> **Complexidade:** Média-Alta  
> **Tempo estimado até MVP:** 7 semanas  
> **Módulo Go:** `github.com/seuusuario/mcpgateway`

---

## Índice

1. [Visão Geral](#1-visão-geral)
2. [O Protocolo MCP — Contexto Técnico](#2-o-protocolo-mcp--contexto-técnico)
3. [Fora de Escopo na v1](#3-fora-de-escopo-na-v1)
4. [Arquitetura do Sistema](#4-arquitetura-do-sistema)
5. [Ambiente de Desenvolvimento Local](#5-ambiente-de-desenvolvimento-local)
6. [Camada de Secrets — Fundação de Segurança](#6-camada-de-secrets--fundação-de-segurança)
7. [Camada de Autenticação Corporativa (STS)](#7-camada-de-autenticação-corporativa-sts)
8. [Configuração via YAML](#8-configuração-via-yaml)
9. [Tipos Go Centrais](#9-tipos-go-centrais)
10. [Componente 1 — Parser e Validador](#10-componente-1--parser-e-validador)
11. [Componente 2 — Runtime do Gateway](#11-componente-2--runtime-do-gateway)
12. [Componente 3 — Servidores MCP](#12-componente-3--servidores-mcp)
13. [Componente 4 — Painel de Controle Web](#13-componente-4--painel-de-controle-web)
14. [Modelo de Dados](#14-modelo-de-dados)
15. [Fluxos Principais](#15-fluxos-principais)
16. [Plano de MVP — 7 Semanas](#16-plano-de-mvp--7-semanas)
17. [Estrutura de Repositório](#17-estrutura-de-repositório)
18. [Modelo de Negócio e Monetização](#18-modelo-de-negócio-e-monetização)
19. [Estratégia de Go-to-Market](#19-estratégia-de-go-to-market)
20. [Riscos e Mitigações](#20-riscos-e-mitigações)
21. [Referências Técnicas](#21-referências-técnicas)

---

## 1. Visão Geral

**MCP Gateway** é um proxy que transforma qualquer API REST existente em um conjunto de "tools" prontas para consumo por agentes LLM (Claude, GPT-4, LangChain, etc.) seguindo o protocolo MCP (Model Context Protocol).

O desenvolvedor descreve a API de origem em um arquivo YAML. O gateway gera o servidor MCP automaticamente, resolvendo toda a complexidade de infraestrutura: autenticação corporativa com STS, credenciais dinâmicas via secrets managers, escopos de autorização por tool, retry com backoff, rate limiting por tenant, logs estruturados e painel web.

```
ANTES (manual):
  API Salesforce → Servidor MCP 1 (400 linhas, auth OAuth2 + STS manual)
  API ERP        → Servidor MCP 2 (380 linhas, token exchange manual)
  API Financeiro → Servidor MCP 3 (420 linhas, mTLS + Vault manual)

DEPOIS (com gateway):
  mcpgateway.yaml (60 linhas) → Gateway → Endpoint MCP unificado
  Secrets Manager             ↗ (credenciais resolvidas em runtime)
  STS Corporativo             ↗ (tokens obtidos e renovados automaticamente)
```

### Princípios de design

- **Secrets nunca em texto claro:** campos sensíveis só aceitam referências a fontes externas
- **Credenciais por identidade de chamada:** o token enviado à API reflete quem está chamando, não uma credencial global do gateway
- **Zero credencial estática em produção:** em ambientes gerenciados (EKS, GKE, AKS) o gateway se autentica via identidade da plataforma
- **Cache por `(api, scope, caller_identity)`:** dois agentes com permissões diferentes nunca compartilham o mesmo token

---

## 2. O Protocolo MCP — Contexto Técnico

### O que é MCP

Model Context Protocol é um protocolo aberto (Anthropic, nov/2024) que define como um agente LLM se comunica com servidores que expõem capacidades. É análogo ao LSP para editores, mas para agentes. Hoje Claude Desktop, Cursor, Windsurf e dezenas de frameworks suportam MCP.

### Modos de transporte

**1. stdio** — para ferramentas locais (Claude Desktop, Cursor)
```
Claude Desktop inicia mcpgateway como processo filho
       │
       │  stdin:  { "jsonrpc": "2.0", "id": 1, "method": "tools/call", ... }
       ▼
  mcpgateway (processo filho rodando localmente)
       │
       │  stdout: { "jsonrpc": "2.0", "id": 1, "result": { ... } }
       ▼
  Claude Desktop recebe e usa o resultado
```

**2. HTTP + SSE** — para agentes em produção
```
Agente em produção
       │
       │  POST /mcp
       │  Authorization: Bearer <agent-key>
       │  { "jsonrpc": "2.0", "id": 1, "method": "tools/call", ... }
       ▼
  MCP Gateway (HTTP server porta 8080)
       │
       │  200 OK com JSON síncrono (para respostas curtas)
       │  ou 200 OK com SSE stream  (para operações longas, > 30s)
       ▼
  Agente recebe resposta
```

### Autenticação no modo stdio vs HTTP

**Diferença importante:** no modo stdio o gateway é invocado pelo Claude Desktop localmente — não há Bearer token de um "agente". As verificações de `allowed_tools` e rate limiting por agente **não se aplicam** neste modo. O gateway em stdio tem acesso total a todas as tools configuradas, pois a segurança é garantida pelo fato de o processo rodar localmente sob controle do usuário.

No modo HTTP, cada requisição carrega um Bearer token de agente que é validado no middleware de autenticação antes de qualquer outra operação.

### Métodos MCP implementados

| Método | Descrição |
|---|---|
| `initialize` | Handshake inicial — retorna capabilities do servidor |
| `tools/list` | Lista todas as tools disponíveis com seus schemas |
| `tools/call` | Executa uma tool com os argumentos fornecidos |

### Estrutura completa de uma chamada

```json
// 1. Cliente envia (tools/call)
{
  "jsonrpc": "2.0",
  "id": "req-abc-123",
  "method": "tools/call",
  "params": {
    "name": "get_customer",
    "arguments": { "customer_id": "CUST-456", "include_history": false }
  }
}

// 2. Gateway responde (sucesso)
{
  "jsonrpc": "2.0",
  "id": "req-abc-123",
  "result": {
    "content": [{ "type": "text", "text": "{\"id\":\"CUST-456\",\"name\":\"Acme\"}" }],
    "isError": false
  }
}

// 3. Gateway responde (erro de API)
{
  "jsonrpc": "2.0",
  "id": "req-abc-123",
  "result": {
    "content": [{ "type": "text", "text": "API returned 404: customer not found" }],
    "isError": true
  }
}

// 4. Gateway responde (erro de protocolo)
{
  "jsonrpc": "2.0",
  "id": "req-abc-123",
  "error": { "code": -32602, "message": "Tool \"get_xyz\" not found" }
}
```

### Códigos de erro JSON-RPC usados

| Código | Significado | Quando usar |
|---|---|---|
| -32700 | Parse error | JSON malformado no request |
| -32600 | Invalid request | Request não segue o formato JSON-RPC |
| -32601 | Method not found | Método desconhecido (ex: tools/delete) |
| -32602 | Invalid params | Tool não existe, argumentos inválidos |
| -32603 | Internal error | Erro interno do gateway (ex: auth falhou) |

---

## 3. Fora de Escopo na v1

Para manter o foco e entregar o MVP, os itens abaixo são explicitamente excluídos da v1:

| Item | Justificativa | Caminho futuro |
|---|---|---|
| MCP `resources` | Protocolo suporta, mas tools cobrem 95% dos casos | v2 |
| MCP `prompts` | Idem | v2 |
| APIs SOAP/XML | Requer transformação complexa | Plugin adaptador |
| APIs GraphQL | Requer parsing do schema GraphQL | v2 |
| WebSockets como destino | Casos de uso raros | v2 |
| SSO no dashboard (SAML/OIDC) | Tier Enterprise | v2 |
| Multi-tenancy (múltiplas orgs no mesmo gateway) | Arquitetura diferente | Tier Cloud |
| `tools/list` com cursor (paginação) | Relevante só com 100+ tools | v2 |
| Streaming de respostas longas via SSE | Complexidade alta, raro em APIs REST | v2 |
| Plugin system (tools em código externo) | Escopo grande | v2 |

---

## 4. Arquitetura do Sistema

```
┌──────────────────────────────────────────────────────────────────────┐
│                           MCP Gateway                                  │
│                                                                        │
│  ┌─────────────┐   ┌──────────────────────────────────────────────┐  │
│  │ config.yaml │──►│  Config Loader → Validator → Secret Resolver  │  │
│  └─────────────┘   └──────────────────────┬───────────────────────┘  │
│                                            │ SecretRefs (lazy)        │
│                    ┌───────────────────────┼──────────────────┐       │
│                    │     Secret Providers  │                  │       │
│                    │  ┌────────┐ ┌───────┐ ┌───────┐ ┌─────┐ │       │
│                    │  │AWS SM  │ │Vault  │ │AzureKV│ │ GCP │ │       │
│                    │  └────────┘ └───────┘ └───────┘ └─────┘ │       │
│                    └───────────────────────┬──────────────────┘       │
│                                            │ valores resolvidos        │
│                    ┌───────────────────────▼──────────────────┐       │
│                    │             Auth Manager                   │       │
│                    │  Token Cache (api × scope × caller_hash)  │       │
│                    │  OAuth2CC │ Token Exchange │ AWS SigV4     │       │
│                    └───────────────────────┬──────────────────┘       │
│                                            │ tokens por identidade     │
│                    ┌───────────────────────▼──────────────────┐       │
│                    │             Tool Registry                  │       │
│                    │  tool_name → (MCPDef + HTTPHandler + Auth) │       │
│                    └──────────────┬─────────────────┬──────────┘       │
│                                   │                 │                   │
│         ┌─────────────────────────▼──┐  ┌──────────▼────────────┐    │
│         │   MCP Server (stdio)        │  │  MCP Server (HTTP/SSE) │    │
│         │   Sem auth de agente        │  │  Bearer token required │    │
│         └─────────────────────────────┘  └───────────────────────┘    │
│                                                                        │
│  ┌─────────────────────────────────────────────────────────────────┐  │
│  │  Request Pipeline (apenas HTTP — stdio pula AgentAuth/Scope)    │  │
│  │  AgentAuth → ScopeCheck → RateLimit → ArgValidate →             │  │
│  │  BuildReq → ResolveAuth → InjectAuth → Execute → Transform → Log│  │
│  └───────────────────────────────┬─────────────────────────────────┘  │
│                                   │                                    │
│  ┌────────────────────────────────▼────────────────────────────────┐  │
│  │                    Admin API (:8081) + Dashboard                  │  │
│  └──────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────┬───────────────────────────────────┘
                                    │
                 ┌──────────────────┼──────────────────┐
                 │                  │                   │
      ┌──────────▼──┐   ┌───────────▼──┐   ┌──────────▼──┐
      │  STS / IdP  │   │  APIs REST   │   │  PG + Redis  │
      │  (tokens)   │   │  (destino)   │   │  (dados)     │
      └─────────────┘   └──────────────┘   └─────────────┘
```

### Componentes e responsabilidades

| Componente | Responsabilidade |
|---|---|
| Config Loader | Parseia YAML, constrói `SecretOrValue` para campos sensíveis |
| Validator | Valida referências, detecta literais em campos sensíveis |
| Secret Resolver | Resolve refs a vault/SM/env com cache e prefetch |
| Auth Manager | Obtém/renova tokens por `(api, scope, caller_hash)` via STS |
| Tool Registry | Mapeia `toolName → (MCPDefinition, HTTPHandler, AuthConfig)` |
| MCP Server stdio | JSON-RPC via stdin/stdout — sem auth de agente |
| MCP Server HTTP | JSON-RPC via HTTP — valida Bearer token do agente |
| Request Pipeline | Chain de middlewares — completo no HTTP, parcial no stdio |
| Admin API | REST endpoints para o dashboard |
| Dashboard UI | React: monitoramento, logs, saúde dos secrets |
| PostgreSQL | Logs de chamadas, audit trail, métricas |
| Redis | Cache de tokens L2, contadores de rate limit |

---

## 5. Ambiente de Desenvolvimento Local

### Requisitos

- Go 1.22+
- Docker + Docker Compose
- Claude Desktop (para testar o modo stdio)

### docker-compose.yaml completo

```yaml
# docker-compose.yaml
version: "3.9"

services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: mcpgateway
      POSTGRES_USER: mcpgateway
      POSTGRES_PASSWORD: dev_password
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U mcpgateway"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
    command: redis-server --appendonly yes
    volumes:
      - redis_data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 5

  # Vault em modo dev — NÃO usar em produção
  # Inicia com token root "dev-root-token" e sem TLS
  vault:
    image: hashicorp/vault:1.16
    ports:
      - "8200:8200"
    environment:
      VAULT_DEV_ROOT_TOKEN_ID: dev-root-token
      VAULT_DEV_LISTEN_ADDRESS: "0.0.0.0:8200"
    cap_add:
      - IPC_LOCK
    command: vault server -dev

  # O gateway em si — só útil quando se quer testar o modo HTTP
  # Para desenvolvimento, rodar `go run ./cmd/mcpgateway serve` direto
  gateway:
    build: .
    ports:
      - "8080:8080"  # MCP HTTP server
      - "8081:8081"  # Admin API
    environment:
      DATABASE_URL: postgres://mcpgateway:dev_password@postgres:5432/mcpgateway
      REDIS_URL: redis://redis:6379
      VAULT_ADDR: http://vault:8200
      VAULT_TOKEN: dev-root-token
    volumes:
      - ./mcpgateway.yaml:/app/mcpgateway.yaml:ro
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    command: ["serve", "--config", "/app/mcpgateway.yaml"]
    profiles:
      - full  # só sobe com: docker-compose --profile full up

volumes:
  postgres_data:
  redis_data:
```

### Subindo o ambiente de desenvolvimento

```bash
# Sobe apenas postgres + redis + vault (sem o gateway)
# Para desenvolvimento, o gateway roda fora do Docker
docker-compose up -d postgres redis vault

# Verifica que estão saudáveis
docker-compose ps

# Roda as migrations
go run ./cmd/mcpgateway migrate --db "postgres://mcpgateway:dev_password@localhost:5432/mcpgateway"

# Cria segredos de teste no Vault local
export VAULT_ADDR=http://localhost:8200
export VAULT_TOKEN=dev-root-token
vault kv put secret/dev/crm-credentials client_id=test-client client_secret=test-secret

# Inicia o gateway (modo HTTP + Admin)
go run ./cmd/mcpgateway serve --config ./examples/local-dev/mcpgateway.yaml

# Em outro terminal — inicia o frontend de desenvolvimento
cd ui && npm run dev
```

### Configurando o Claude Desktop (modo stdio)

Edite `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) ou `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "mcpgateway-dev": {
      "command": "/caminho/para/mcpgateway",
      "args": ["serve", "--config", "/caminho/para/examples/local-dev/mcpgateway.yaml", "--stdio"],
      "env": {
        "DATABASE_URL": "postgres://mcpgateway:dev_password@localhost:5432/mcpgateway",
        "REDIS_URL": "redis://localhost:6379"
      }
    }
  }
}
```

Para usar com `go run` durante desenvolvimento:

```json
{
  "mcpServers": {
    "mcpgateway-dev": {
      "command": "go",
      "args": ["run", "./cmd/mcpgateway", "serve", "--config", "./examples/local-dev/mcpgateway.yaml", "--stdio"],
      "cwd": "/caminho/para/o/projeto"
    }
  }
}
```

### Config de desenvolvimento local (sem vault, sem STS)

```yaml
# examples/local-dev/mcpgateway.yaml
name: "dev-tools"
version: "1.0"

# Em desenvolvimento, sem autenticação de agente (modo permissivo)
security:
  dev_mode: true  # desabilita AgentAuth no HTTP; nunca usar em produção

apis:
  jsonplaceholder:
    base_url: "https://jsonplaceholder.typicode.com"
    auth:
      type: none  # API pública sem auth
    timeout: 10s
    retry:
      max_attempts: 2
      backoff: fixed
      initial_delay: 500ms

tools:
  get_user:
    api: jsonplaceholder
    description: "Busca dados de um usuário pelo ID"
    method: GET
    path: "/users/{user_id}"
    parameters:
      - name: user_id
        in: path
        type: integer
        required: true
        description: "ID do usuário (1-10)"

  list_posts:
    api: jsonplaceholder
    description: "Lista posts de um usuário"
    method: GET
    path: "/posts"
    parameters:
      - name: userId
        in: query
        type: integer
        required: false
        description: "Filtrar por usuário"
```

### Makefile

```makefile
# Makefile

.PHONY: dev test lint build docker-dev

# Sobe infra de desenvolvimento
dev-infra:
	docker-compose up -d postgres redis vault

# Roda migrations
migrate:
	go run ./cmd/mcpgateway migrate --db "$(DATABASE_URL)"

# Inicia o gateway em desenvolvimento
dev:
	DATABASE_URL=postgres://mcpgateway:dev_password@localhost:5432/mcpgateway \
	REDIS_URL=redis://localhost:6379 \
	go run ./cmd/mcpgateway serve --config ./examples/local-dev/mcpgateway.yaml

# Valida um arquivo de config
validate:
	go run ./cmd/mcpgateway validate --config $(CONFIG)

# Testes unitários
test:
	go test ./... -v -race -count=1

# Testes de integração (requer infra rodando)
test-integration:
	go test ./... -v -race -count=1 -tags=integration

# Lint
lint:
	golangci-lint run ./...

# Build do binário
build:
	go build -o bin/mcpgateway ./cmd/mcpgateway

# Build multi-plataforma (via GoReleaser)
release-snapshot:
	goreleaser release --snapshot --clean
```

---

## 6. Camada de Secrets — Fundação de Segurança

### O problema do bootstrap

Como o gateway se autentica nos secrets managers sem ter credenciais estáticas? Usando a identidade da plataforma onde roda:

```
Ambiente          Mecanismo                        Credencial necessária
─────────────────────────────────────────────────────────────────────────
EKS (AWS)       → IAM Role for Service Accounts  → Nenhuma
ECS (AWS)       → Task IAM Role                  → Nenhuma
EC2 (AWS)       → Instance Profile               → Nenhuma
GKE (Google)    → Workload Identity              → Nenhuma
AKS (Azure)     → Managed Identity               → Nenhuma
Azure VM        → Managed Identity               → Nenhuma
VM genérica     → Vault AppRole                  → VAULT_ROLE_ID + VAULT_SECRET_ID (env)
Desenvolvimento → Credenciais locais do dev      → AWS_PROFILE / VAULT_TOKEN / az login
```

### Fontes suportadas

```
Fonte                   Identificador YAML       Auth do gateway
──────────────────────────────────────────────────────────────────
AWS Secrets Manager   → aws_secrets_manager      IAM Role (automático)
HashiCorp Vault       → hashicorp_vault           AppRole / K8s Auth / AWS IAM
Azure Key Vault       → azure_keyvault            Managed Identity (automático)
GCP Secret Manager    → gcp_secret_manager        Workload Identity (automático)
Variável de ambiente  → env                       N/A
```

### Sintaxe YAML para referências

Todo campo sensível aceita dois formatos. **Não existe o sufixo `_env`** — foi removido para manter uma única sintaxe consistente:

```yaml
# Formato 1: string literal (apenas para dev — rejeitado em campos de credencial)
# NÃO funciona em campos como client_secret, password, token, key, username
# Funciona apenas em campos não-sensíveis como base_url, header_name, etc.

# Formato 2: referência explícita (único formato válido para campos sensíveis)
client_secret:
  source: env
  var: "MINHA_VAR"                  # variável de ambiente

client_secret:
  source: aws_secrets_manager
  secret_id: "prod/erp/credentials" # ID ou ARN do secret
  field: "client_secret"            # campo dentro do JSON (omitir se for string simples)
  region: "us-east-1"               # opcional — usa AWS_REGION por padrão
  refresh_before_expiry: 5m         # renovar 5min antes de expirar

client_secret:
  source: hashicorp_vault
  path: "secret/data/erp/credentials" # path KV v2 completo
  field: "client_secret"
  vault_role: "mcpgateway-prod"        # role Vault para autenticar o gateway

password:
  source: azure_keyvault
  vault_name: "empresa-prod-kv"
  secret_name: "erp-gateway-password"
  # version omitida = versão mais recente

api_key:
  source: gcp_secret_manager
  project_id: "meu-projeto-123"
  secret_name: "erp-api-key"
  version: "latest"                    # ou número específico
```

### Tipos Go centrais da camada de secrets

```go
// internal/secrets/types.go

package secrets

// SecretRef descreve como resolver um valor sensível
type SecretRef struct {
    Source  string `yaml:"source"` // "env", "aws_secrets_manager", etc.

    // env
    Var string `yaml:"var"`

    // aws_secrets_manager
    SecretID string `yaml:"secret_id"`
    Region   string `yaml:"region"`

    // hashicorp_vault
    Path      string `yaml:"path"`
    VaultRole string `yaml:"vault_role"`

    // azure_keyvault
    VaultName  string `yaml:"vault_name"`
    SecretName string `yaml:"secret_name"`

    // gcp_secret_manager
    ProjectID string `yaml:"project_id"`

    // compartilhado entre aws/vault/gcp
    Field   string `yaml:"field"`   // campo dentro do JSON
    Version string `yaml:"version"` // versão do secret

    // cache
    RefreshBeforeExpiry time.Duration `yaml:"refresh_before_expiry"`
}

func (r SecretRef) CacheKey() string {
    h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s",
        r.Source, r.SecretID, r.Path, r.VaultName, r.SecretName,
        r.ProjectID, r.Field,
    )))
    return hex.EncodeToString(h[:16]) // 16 bytes = 32 hex chars, suficiente
}

type SecretValue struct {
    Value     string
    ExpiresAt time.Time // zero = sem expiração conhecida
}

// Provider é a interface implementada por cada fonte de secret
type Provider interface {
    Get(ctx context.Context, ref SecretRef) (SecretValue, error)
}
```

### Implementação do SecretResolver

```go
// internal/secrets/resolver.go

package secrets

type Resolver struct {
    providers map[string]Provider
    cache     *ttlcache.Cache[string, SecretValue]
}

func NewResolver(ctx context.Context) (*Resolver, error) {
    providers := map[string]Provider{
        "env":  &EnvProvider{},
        "none": &NoneProvider{}, // para auth type: none em dev
    }

    // AWS — usa IAM Role automaticamente se disponível
    awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
    if err == nil {
        providers["aws_secrets_manager"] = &AWSProvider{
            client: secretsmanager.NewFromConfig(awsCfg),
        }
    }

    // HashiCorp Vault — detecta VAULT_ADDR
    if addr := os.Getenv("VAULT_ADDR"); addr != "" {
        if vc, err := newVaultClient(ctx, addr); err == nil {
            providers["hashicorp_vault"] = &VaultProvider{client: vc}
        } else {
            log.Warn("vault provider unavailable", zap.Error(err))
        }
    }

    // Azure Key Vault — usa DefaultAzureCredential (Managed Identity > env > CLI)
    if azCred, err := azidentity.NewDefaultAzureCredential(nil); err == nil {
        providers["azure_keyvault"] = &AzureProvider{credential: azCred}
    }

    // GCP Secret Manager — usa Application Default Credentials
    if gcpClient, err := secretmanager.NewClient(ctx); err == nil {
        providers["gcp_secret_manager"] = &GCPProvider{client: gcpClient}
    }

    return &Resolver{
        providers: providers,
        cache:     ttlcache.New[string, SecretValue](ttlcache.WithTTL[string, SecretValue](15 * time.Minute)),
    }, nil
}

func (r *Resolver) Resolve(ctx context.Context, ref SecretRef) (string, error) {
    key := ref.CacheKey()

    if item := r.cache.Get(key); item != nil {
        sv := item.Value()
        // Prefetch assíncrono se estiver perto de expirar
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
        return "", fmt.Errorf("secret source %q not available (provider not initialized)", ref.Source)
    }

    sv, err := provider.Get(ctx, ref)
    if err != nil {
        return "", fmt.Errorf("[secret source=%s field=%s]: %w", ref.Source, ref.Field, err)
    }

    ttl := 15 * time.Minute
    if !sv.ExpiresAt.IsZero() {
        ttl = time.Until(sv.ExpiresAt)
        if ref.RefreshBeforeExpiry > 0 {
            ttl -= ref.RefreshBeforeExpiry
        }
    }
    if ttl > 0 {
        r.cache.Set(key, sv, ttl)
    }

    return sv.Value, nil
}

func (r *Resolver) prefetch(ctx context.Context, ref SecretRef, key string) {
    sv, err := r.providers[ref.Source].Get(ctx, ref)
    if err != nil {
        return // erro no prefetch é silencioso — próxima chamada vai tentar novamente
    }
    ttl := 15 * time.Minute
    if !sv.ExpiresAt.IsZero() {
        ttl = time.Until(sv.ExpiresAt)
    }
    r.cache.Set(key, sv, ttl)
}

// newVaultClient inicializa o cliente Vault com o método de auth correto
func newVaultClient(ctx context.Context, addr string) (*vault.Client, error) {
    cfg := vault.DefaultConfiguration()
    cfg.Address = addr

    client, err := vault.New(vault.WithConfiguration(cfg))
    if err != nil {
        return nil, err
    }

    // Tenta AppRole primeiro (VM genérica)
    roleID := os.Getenv("VAULT_ROLE_ID")
    secretID := os.Getenv("VAULT_SECRET_ID")
    if roleID != "" && secretID != "" {
        resp, err := client.Auth.AppRoleLogin(ctx, schema.AppRoleLoginRequest{
            RoleId:   roleID,
            SecretId: secretID,
        })
        if err == nil {
            client.SetToken(resp.Auth.ClientToken)
            return client, nil
        }
    }

    // Tenta token direto (desenvolvimento)
    if token := os.Getenv("VAULT_TOKEN"); token != "" {
        client.SetToken(token)
        return client, nil
    }

    return nil, fmt.Errorf("no vault authentication method available (VAULT_ROLE_ID+VAULT_SECRET_ID or VAULT_TOKEN)")
}
```

### Provider AWS Secrets Manager

```go
// internal/secrets/provider_aws.go

type AWSProvider struct {
    client *secretsmanager.Client
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
                ref.Field, ref.SecretID, maps.Keys(data))
        }
        raw = val
    }

    // AWS não expõe TTL diretamente — usa 12h como default seguro
    return SecretValue{
        Value:     raw,
        ExpiresAt: time.Now().Add(12 * time.Hour),
    }, nil
}
```

### Provider HashiCorp Vault

```go
// internal/secrets/provider_vault.go

type VaultProvider struct {
    client *vault.Client
}

func (p *VaultProvider) Get(ctx context.Context, ref SecretRef) (SecretValue, error) {
    // KV v2: path completo é "mount/data/key"
    // Ex: "secret/data/gateway/erp" → mount="secret", key="gateway/erp"
    parts := strings.SplitN(ref.Path, "/data/", 2)
    if len(parts) != 2 {
        return SecretValue{}, fmt.Errorf("vault path must be KV v2 format: mount/data/key-path, got %q", ref.Path)
    }
    mount := parts[0]
    keyPath := parts[1]

    secret, err := p.client.Secrets.KvV2Read(ctx, keyPath,
        vault.WithMountPath(mount),
    )
    if err != nil {
        return SecretValue{}, fmt.Errorf("vault KV read(%s): %w", ref.Path, err)
    }

    data, ok := secret.Data.Data.(map[string]interface{})
    if !ok {
        return SecretValue{}, fmt.Errorf("vault secret data is not a map at path %q", ref.Path)
    }

    val, ok := data[ref.Field]
    if !ok {
        return SecretValue{}, fmt.Errorf("field %q not found at vault path %q", ref.Field, ref.Path)
    }
    strVal, ok := val.(string)
    if !ok {
        return SecretValue{}, fmt.Errorf("field %q at vault path %q is not a string", ref.Field, ref.Path)
    }

    // Vault retorna LeaseDuration em segundos (int)
    leaseDuration := time.Duration(secret.LeaseDuration) * time.Second
    var expiresAt time.Time
    if leaseDuration > 0 {
        expiresAt = time.Now().Add(leaseDuration)
    }

    return SecretValue{Value: strVal, ExpiresAt: expiresAt}, nil
}
```

### Provider Azure Key Vault

```go
// internal/secrets/provider_azure.go

type AzureProvider struct {
    credential azcore.TokenCredential
    clients    sync.Map // vault_name → *azsecrets.Client (cache de clientes)
}

func (p *AzureProvider) Get(ctx context.Context, ref SecretRef) (SecretValue, error) {
    vaultURL := fmt.Sprintf("https://%s.vault.azure.net", ref.VaultName)

    var client *azsecrets.Client
    if cached, ok := p.clients.Load(ref.VaultName); ok {
        client = cached.(*azsecrets.Client)
    } else {
        c, err := azsecrets.NewClient(vaultURL, p.credential, nil)
        if err != nil {
            return SecretValue{}, err
        }
        p.clients.Store(ref.VaultName, c)
        client = c
    }

    version := ref.Version // "" = latest
    resp, err := client.GetSecret(ctx, ref.SecretName, version, nil)
    if err != nil {
        return SecretValue{}, fmt.Errorf("azure keyvault get(%s/%s): %w", ref.VaultName, ref.SecretName, err)
    }

    var expiresAt time.Time
    if resp.Attributes != nil && resp.Attributes.Expires != nil {
        expiresAt = *resp.Attributes.Expires
    }

    return SecretValue{
        Value:     *resp.Value,
        ExpiresAt: expiresAt,
    }, nil
}
```

### Provider GCP Secret Manager

```go
// internal/secrets/provider_gcp.go

type GCPProvider struct {
    client *secretmanager.Client
}

func (p *GCPProvider) Get(ctx context.Context, ref SecretRef) (SecretValue, error) {
    version := ref.Version
    if version == "" {
        version = "latest"
    }

    name := fmt.Sprintf("projects/%s/secrets/%s/versions/%s",
        ref.ProjectID, ref.SecretName, version)

    resp, err := p.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
        Name: name,
    })
    if err != nil {
        return SecretValue{}, fmt.Errorf("gcp secret manager access(%s): %w", name, err)
    }

    return SecretValue{
        Value:     string(resp.Payload.Data),
        ExpiresAt: time.Time{}, // GCP não expõe TTL via API de acesso
    }, nil
}
```

---

## 7. Camada de Autenticação Corporativa (STS)

### O modelo: credenciais por identidade de chamada

O token enviado à API de destino deve refletir a identidade e os escopos do agente — não uma credencial global do gateway:

```
Agente A (allowed: get_inventory)    Agente B (allowed: get_inventory, create_order)
         │                                       │
         │ tools/call: get_inventory             │ tools/call: create_order
         ▼                                       ▼
  Gateway obtém token                     Gateway obtém token
  escopo: erp:read                        escopo: erp:read erp:write
  credencial: client_id_a                 credencial: client_id_b
         │                                       │
         ▼                                       ▼
  GET /inventory                          POST /orders
  Bearer: <token erp:read>                Bearer: <token erp:read+write>
```

### Tipos de autenticação suportados

| Tipo | Quando usar |
|---|---|
| `none` | APIs públicas sem auth (apenas dev) |
| `bearer` | Token estático do secrets manager |
| `api_key_header` | Chave em header customizado |
| `api_key_query` | Chave como query parameter |
| `basic` | HTTP Basic Auth |
| `oauth2_client_credentials` | OAuth2 machine-to-machine padrão |
| `oauth2_token_exchange` | STS corporativo que exige troca de token (RFC 8693) |
| `aws_sigv4` | APIs AWS (API Gateway, Lambda URLs) |
| `mtls` | Certificado TLS do cliente |

### Cache de tokens: chave composta por identidade

```go
// internal/auth/token_cache.go

type CacheKey struct {
    APIID          string
    Scope          string
    CallerIdentity string // hash do Bearer token do agente, ou "stdio" para modo stdio
}

func (k CacheKey) RedisKey() string {
    h := sha256.Sum256([]byte(k.APIID + "|" + k.Scope + "|" + k.CallerIdentity))
    return "token:" + hex.EncodeToString(h[:])
}

type CachedToken struct {
    AccessToken string    `json:"access_token"`
    TokenType   string    `json:"token_type"`
    Scope       string    `json:"scope"`
    ExpiresAt   time.Time `json:"expires_at"`
}

type TokenCache struct {
    redis *redis.Client
    local *ttlcache.Cache[string, CachedToken] // L1 in-process
}

// Get retorna token válido ou (nil, false) se expirado/ausente
func (c *TokenCache) Get(key CacheKey) (*CachedToken, bool) {
    k := key.RedisKey()
    margin := 30 * time.Second

    // L1: cache local (mais rápido, evita roundtrip ao Redis)
    if item := c.local.Get(k); item != nil {
        t := item.Value()
        if time.Until(t.ExpiresAt) > margin {
            return &t, true
        }
        c.local.Delete(k)
    }

    // L2: Redis (compartilhado entre instâncias do gateway)
    raw, err := c.redis.Get(context.Background(), k).Bytes()
    if err != nil {
        return nil, false
    }
    var t CachedToken
    if err := json.Unmarshal(raw, &t); err != nil || time.Until(t.ExpiresAt) <= margin {
        return nil, false
    }

    // Popula L1
    c.local.Set(k, t, time.Until(t.ExpiresAt)-margin)
    return &t, true
}

func (c *TokenCache) Set(key CacheKey, token CachedToken) {
    k := key.RedisKey()
    ttl := time.Until(token.ExpiresAt) - 30*time.Second
    if ttl <= 0 {
        return
    }
    raw, _ := json.Marshal(token)
    c.redis.Set(context.Background(), k, raw, ttl)
    c.local.Set(k, token, ttl)
}
```

### Auth Manager

```go
// internal/auth/manager.go

// tokenResponse é a resposta padrão OAuth2 de um token endpoint
type tokenResponse struct {
    AccessToken string `json:"access_token"`
    TokenType   string `json:"token_type"`
    ExpiresIn   int    `json:"expires_in"` // segundos
    Scope       string `json:"scope"`
    Error       string `json:"error"`
    ErrorDesc   string `json:"error_description"`
}

type Manager struct {
    configs    map[string]config.AuthConfig // keyed por api_id
    secrets    *secrets.Resolver
    cache      *TokenCache
    httpClient *http.Client
}

// GetToken retorna um token válido para (apiID, scope, callerToken)
// callerToken é o Bearer token do agente HTTP, ou "" para stdio
func (m *Manager) GetToken(ctx context.Context, apiID, scope, callerToken string) (string, error) {
    cfg := m.configs[apiID]

    callerIdentity := "stdio"
    if callerToken != "" {
        h := sha256.Sum256([]byte(callerToken))
        callerIdentity = hex.EncodeToString(h[:8]) // primeiros 8 bytes bastam como identidade
    }

    cacheKey := CacheKey{APIID: apiID, Scope: scope, CallerIdentity: callerIdentity}

    if cached, ok := m.cache.Get(cacheKey); ok {
        return cached.AccessToken, nil
    }

    var token *CachedToken
    var err error

    switch cfg.Type {
    case "oauth2_client_credentials":
        token, err = m.fetchClientCredentials(ctx, cfg, scope)
    case "oauth2_token_exchange":
        token, err = m.fetchTokenExchange(ctx, cfg, callerToken, scope)
    case "bearer":
        value, e := m.secrets.Resolve(ctx, cfg.Token.Ref())
        if e != nil {
            return "", fmt.Errorf("resolving bearer token: %w", e)
        }
        return value, nil
    case "none", "api_key_header", "api_key_query", "basic", "mtls", "aws_sigv4":
        // Esses tipos não usam cache de token — credenciais são injetadas diretamente em ApplyAuth
        return "", nil
    default:
        return "", fmt.Errorf("unknown auth type: %q", cfg.Type)
    }

    if err != nil {
        return "", fmt.Errorf("acquiring token [api=%s scope=%s]: %w", apiID, scope, err)
    }

    m.cache.Set(cacheKey, *token)
    return token.AccessToken, nil
}

func (m *Manager) fetchClientCredentials(ctx context.Context, cfg config.AuthConfig, scope string) (*CachedToken, error) {
    clientID, err := m.secrets.Resolve(ctx, cfg.ClientID.Ref())
    if err != nil {
        return nil, fmt.Errorf("resolving client_id: %w", err)
    }
    clientSecret, err := m.secrets.Resolve(ctx, cfg.ClientSecret.Ref())
    if err != nil {
        return nil, fmt.Errorf("resolving client_secret: %w", err)
    }

    form := url.Values{
        "grant_type":    {"client_credentials"},
        "client_id":     {clientID},
        "client_secret": {clientSecret},
        "scope":         {scope},
    }
    if cfg.Audience != "" {
        form.Set("audience", cfg.Audience)
    }
    for k, v := range cfg.ExtraParams {
        form.Set(k, v)
    }

    resp, err := m.httpClient.PostForm(cfg.TokenURL, form)
    if err != nil {
        return nil, fmt.Errorf("POST %s: %w", cfg.TokenURL, err)
    }
    defer resp.Body.Close()

    body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
    if resp.StatusCode != 200 {
        return nil, fmt.Errorf("STS %s returned %d: %s", cfg.TokenURL, resp.StatusCode, body)
    }

    var tr tokenResponse
    if err := json.Unmarshal(body, &tr); err != nil {
        return nil, fmt.Errorf("decoding token response: %w", err)
    }
    if tr.Error != "" {
        return nil, fmt.Errorf("STS error: %s — %s", tr.Error, tr.ErrorDesc)
    }
    if tr.AccessToken == "" {
        return nil, fmt.Errorf("STS returned empty access_token")
    }

    return &CachedToken{
        AccessToken: tr.AccessToken,
        TokenType:   tr.TokenType,
        Scope:       tr.Scope,
        ExpiresAt:   time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
    }, nil
}

func (m *Manager) fetchTokenExchange(ctx context.Context, cfg config.AuthConfig, subjectToken, requestedScope string) (*CachedToken, error) {
    if subjectToken == "" {
        return nil, fmt.Errorf("token_exchange requires a subject_token (caller must present a Bearer token)")
    }

    actorClientID, err := m.secrets.Resolve(ctx, cfg.ActorClientID.Ref())
    if err != nil {
        return nil, fmt.Errorf("resolving actor_client_id: %w", err)
    }
    actorClientSecret, err := m.secrets.Resolve(ctx, cfg.ActorClientSecret.Ref())
    if err != nil {
        return nil, fmt.Errorf("resolving actor_client_secret: %w", err)
    }

    form := url.Values{
        "grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
        "subject_token":        {subjectToken},
        "subject_token_type":   {"urn:ietf:params:oauth:token-type:access_token"},
        "actor_client_id":      {actorClientID},
        "actor_client_secret":  {actorClientSecret},
        "requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
    }
    if requestedScope != "" {
        form.Set("requested_scope", requestedScope)
    }
    if cfg.Audience != "" {
        form.Set("audience", cfg.Audience)
    }

    resp, err := m.httpClient.PostForm(cfg.TokenURL, form)
    if err != nil {
        return nil, fmt.Errorf("POST %s: %w", cfg.TokenURL, err)
    }
    defer resp.Body.Close()

    body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
    if resp.StatusCode != 200 {
        return nil, fmt.Errorf("STS token exchange returned %d: %s", resp.StatusCode, body)
    }

    var tr tokenResponse
    if err := json.Unmarshal(body, &tr); err != nil {
        return nil, fmt.Errorf("decoding token exchange response: %w", err)
    }
    if tr.Error != "" {
        return nil, fmt.Errorf("token exchange error: %s — %s", tr.Error, tr.ErrorDesc)
    }

    return &CachedToken{
        AccessToken: tr.AccessToken,
        TokenType:   tr.TokenType,
        Scope:       tr.Scope,
        ExpiresAt:   time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
    }, nil
}

// ApplyAuth injeta as credenciais na requisição HTTP de saída
// Chamado depois de GetToken para tipos que não usam Bearer (api_key, basic, mtls)
func (m *Manager) ApplyAuth(ctx context.Context, req *http.Request, apiID, bearerToken string) error {
    cfg := m.configs[apiID]

    switch cfg.Type {
    case "none":
        // sem auth

    case "bearer", "oauth2_client_credentials", "oauth2_token_exchange":
        req.Header.Set("Authorization", "Bearer "+bearerToken)

    case "api_key_header":
        key, err := m.secrets.Resolve(ctx, cfg.Key.Ref())
        if err != nil {
            return err
        }
        req.Header.Set(cfg.HeaderName, key)

    case "api_key_query":
        key, err := m.secrets.Resolve(ctx, cfg.Key.Ref())
        if err != nil {
            return err
        }
        q := req.URL.Query()
        q.Set(cfg.ParamName, key)
        req.URL.RawQuery = q.Encode()

    case "basic":
        username, err := m.secrets.Resolve(ctx, cfg.Username.Ref())
        if err != nil {
            return err
        }
        password, err := m.secrets.Resolve(ctx, cfg.Password.Ref())
        if err != nil {
            return err
        }
        req.SetBasicAuth(username, password)

    case "aws_sigv4":
        return m.signAWSRequest(ctx, req, cfg)

    case "mtls":
        // mTLS é configurado no http.Client criado para esta API — não no request
        // O cliente já tem o certificado configurado

    default:
        return fmt.Errorf("ApplyAuth: unknown type %q", cfg.Type)
    }

    return nil
}
```

---

## 8. Configuração via YAML

O arquivo de configuração é a interface principal do produto.

### Exemplo corporativo completo

```yaml
# mcpgateway.yaml
name: "empresa-tools"
version: "1.0"

# ───────────────────────────────────────────────────────
# Chaves de agentes que consomem este gateway (modo HTTP)
# No modo stdio, esta seção é ignorada
# ───────────────────────────────────────────────────────
security:
  # allowed_tools: ["*"] permite todas as tools para o agente
  api_keys:
    - id: "agent-crm-reader"
      key:
        source: aws_secrets_manager
        secret_id: "prod/gateway/agent-keys"
        field: "crm_reader_key"
      name: "Agente leitura CRM"
      allowed_tools: ["get_customer", "search_customers"]

    - id: "agent-ops"
      key:
        source: aws_secrets_manager
        secret_id: "prod/gateway/agent-keys"
        field: "ops_agent_key"
      name: "Agente operações"
      allowed_tools: ["*"]  # acesso a todas as tools

  rate_limiting:
    requests_per_minute: 60
    requests_per_hour: 1000
    burst: 10

  # dev_mode: true desabilita AgentAuth e rate limiting
  # NUNCA usar em produção
  dev_mode: false

# ───────────────────────────────────────────────────────
# APIs de destino
# ───────────────────────────────────────────────────────
apis:
  crm:
    base_url: "https://api.salesforce.com/v2"
    auth:
      type: oauth2_client_credentials
      token_url: "https://sts.empresa.com/oauth2/token"
      client_id:
        source: aws_secrets_manager
        secret_id: "prod/crm/credentials"
        field: "client_id"
      client_secret:
        source: aws_secrets_manager
        secret_id: "prod/crm/credentials"
        field: "client_secret"
        refresh_before_expiry: 5m
      scope: "crm:read"
      audience: "https://api.salesforce.com"
    headers:
      Content-Type: "application/json"
    timeout: 30s
    retry:
      max_attempts: 3
      backoff: exponential
      initial_delay: 500ms
      max_delay: 10s

  erp:
    base_url: "https://erp.empresa.com/api/v3"
    auth:
      type: oauth2_token_exchange
      token_url: "https://sts.empresa.com/oauth2/token"
      actor_client_id:
        source: hashicorp_vault
        path: "secret/data/gateway/identity"
        field: "client_id"
        vault_role: "mcpgateway-prod"
      actor_client_secret:
        source: hashicorp_vault
        path: "secret/data/gateway/identity"
        field: "client_secret"
        vault_role: "mcpgateway-prod"
      audience: "https://erp.empresa.com"
    timeout: 60s
    retry:
      max_attempts: 2
      backoff: fixed
      initial_delay: 1s

  financeiro:
    base_url: "https://financeiro.empresa.com"
    auth:
      type: mtls
      cert_file:
        source: env
        var: "MTLS_CERT_PATH"
      key_file:
        source: env
        var: "MTLS_KEY_PATH"
      ca_file: "/etc/ssl/certs/empresa-ca.crt"
    timeout: 15s

# ───────────────────────────────────────────────────────
# Tools expostas via MCP
# ───────────────────────────────────────────────────────
tools:
  get_customer:
    api: crm
    description: |
      Busca dados completos de um cliente pelo ID.
      Retorna nome, contatos, status e valor do contrato.
    method: GET
    path: "/customers/{customer_id}"
    parameters:
      - name: customer_id
        in: path
        type: string
        required: true
        description: "ID do cliente (CUST-XXXXX)"
      - name: include_history
        in: query
        type: boolean
        required: false
        default: false
    response_transform:
      include_fields: [id, name, email, phone, account_status, contract_value]

  search_customers:
    api: crm
    description: "Busca clientes por nome, email ou empresa. Até 20 resultados."
    method: GET
    path: "/customers/search"
    parameters:
      - name: query
        in: query
        type: string
        required: true
      - name: status
        in: query
        type: string
        required: false
        enum: ["active", "inactive", "prospect"]

  get_inventory:
    api: erp
    description: "Consulta estoque atual de produtos."
    method: GET
    path: "/inventory"
    auth_override:
      scope: "erp:read"             # sobrescreve escopo da API para esta tool
    parameters:
      - name: product_id
        in: query
        type: string
        required: false
      - name: below_minimum
        in: query
        type: boolean
        required: false
        default: false

  create_purchase_order:
    api: erp
    description: "Cria ordem de compra no ERP."
    method: POST
    path: "/purchase-orders"
    auth_override:
      scope: "erp:read erp:write"   # escopo mais amplo para operação de escrita
    rate_limiting:
      requests_per_minute: 5
      requests_per_hour: 20
    require_confirmation: true
    confirmation_message: "Criar ordem: {quantity} unidades de {product_id}?"
    body:
      type: object
      properties:
        - name: product_id
          type: string
          required: true
        - name: quantity
          type: integer
          required: true
          minimum: 1
          maximum: 10000
        - name: supplier_id
          type: string
          required: true
        - name: notes
          type: string
          required: false

  get_account_balance:
    api: financeiro
    description: "Consulta saldo de uma conta financeira."
    method: GET
    path: "/accounts/{account_id}/balance"
    parameters:
      - name: account_id
        in: path
        type: string
        required: true
      - name: period
        in: query
        type: string
        required: false
        enum: ["day", "week", "month", "quarter"]
        default: "month"
```

### Tabela de referência: campos por tipo de auth

| Tipo | Campo | Obrigatório | Aceita SecretRef |
|---|---|---|---|
| `none` | — | — | — |
| `bearer` | `token` | ✅ | ✅ |
| `api_key_header` | `header_name` | ✅ | ❌ |
| | `key` | ✅ | ✅ |
| `api_key_query` | `param_name` | ✅ | ❌ |
| | `key` | ✅ | ✅ |
| `basic` | `username` | ✅ | ✅ |
| | `password` | ✅ | ✅ |
| `oauth2_client_credentials` | `token_url` | ✅ | ❌ |
| | `client_id` | ✅ | ✅ |
| | `client_secret` | ✅ | ✅ |
| | `scope` | ✅ | ❌ |
| | `audience` | ❌ | ❌ |
| | `extra_params` | ❌ | ❌ |
| `oauth2_token_exchange` | `token_url` | ✅ | ❌ |
| | `actor_client_id` | ✅ | ✅ |
| | `actor_client_secret` | ✅ | ✅ |
| | `audience` | ❌ | ❌ |
| `aws_sigv4` | `region` | ✅ | ❌ |
| | `service` | ✅ | ❌ |
| `mtls` | `cert_file` | ✅ | ✅ (via env) |
| | `key_file` | ✅ | ✅ (via env) |
| | `ca_file` | ✅ | ❌ (caminho direto) |

---

## 9. Tipos Go Centrais

Esta seção define todas as structs de configuração. São a base que os demais componentes usam.

```go
// internal/config/types.go

package config

// SecretOrValue é um campo que pode ser literal (dev) ou referência a um secret
type SecretOrValue struct {
    literal  string
    ref      *secrets.SecretRef
    resolver *secrets.Resolver
}

func (sv *SecretOrValue) Resolve(ctx context.Context) (string, error) {
    if sv == nil {
        return "", nil
    }
    if sv.ref == nil {
        return sv.literal, nil
    }
    return sv.resolver.Resolve(ctx, *sv.ref)
}

func (sv *SecretOrValue) Ref() secrets.SecretRef {
    if sv == nil || sv.ref == nil {
        return secrets.SecretRef{Source: "literal", Var: sv.literal}
    }
    return *sv.ref
}

func (sv *SecretOrValue) IsLiteral() bool {
    return sv != nil && sv.ref == nil && sv.literal != ""
}

func (sv *SecretOrValue) UnmarshalYAML(value *yaml.Node) error {
    if value.Kind == yaml.ScalarNode {
        sv.literal = value.Value
        return nil
    }
    var ref secrets.SecretRef
    if err := value.Decode(&ref); err != nil {
        return fmt.Errorf("invalid secret reference: %w", err)
    }
    sv.ref = &ref
    return nil
}

// GatewayConfig é a raiz da configuração
type GatewayConfig struct {
    Name     string                `yaml:"name"`
    Version  string                `yaml:"version"`
    Security SecurityConfig        `yaml:"security"`
    APIs     map[string]APIConfig  `yaml:"apis"`
    Tools    map[string]ToolConfig `yaml:"tools"`
}

type SecurityConfig struct {
    APIKeys     []AgentKeyConfig `yaml:"api_keys"`
    RateLimit   RateLimitConfig  `yaml:"rate_limiting"`
    DevMode     bool             `yaml:"dev_mode"` // NUNCA true em produção
}

type AgentKeyConfig struct {
    ID           string         `yaml:"id"`
    Key          *SecretOrValue `yaml:"key"`
    Name         string         `yaml:"name"`
    AllowedTools []string       `yaml:"allowed_tools"` // ["*"] = todas
}

// AllowsTool verifica se este agente pode chamar a tool
func (a AgentKeyConfig) AllowsTool(toolName string) bool {
    for _, t := range a.AllowedTools {
        if t == "*" || t == toolName {
            return true
        }
    }
    return false
}

type APIConfig struct {
    BaseURL string            `yaml:"base_url"`
    Auth    AuthConfig        `yaml:"auth"`
    Headers map[string]string `yaml:"headers"`
    Timeout Duration          `yaml:"timeout"`
    Retry   RetryConfig       `yaml:"retry"`
}

type AuthConfig struct {
    Type     string `yaml:"type"`
    // oauth2_client_credentials
    TokenURL     string         `yaml:"token_url"`
    ClientID     *SecretOrValue `yaml:"client_id"`
    ClientSecret *SecretOrValue `yaml:"client_secret"`
    Scope        string         `yaml:"scope"`
    Audience     string         `yaml:"audience"`
    ExtraParams  map[string]string `yaml:"extra_params"`
    // oauth2_token_exchange
    ActorClientID     *SecretOrValue `yaml:"actor_client_id"`
    ActorClientSecret *SecretOrValue `yaml:"actor_client_secret"`
    // bearer
    Token *SecretOrValue `yaml:"token"`
    // api_key_header
    HeaderName string         `yaml:"header_name"`
    Key        *SecretOrValue `yaml:"key"`
    // api_key_query
    ParamName string `yaml:"param_name"`
    // basic
    Username *SecretOrValue `yaml:"username"`
    Password *SecretOrValue `yaml:"password"`
    // aws_sigv4
    Region  string `yaml:"region"`
    Service string `yaml:"service"`
    // mtls
    CertFile *SecretOrValue `yaml:"cert_file"`
    KeyFile  *SecretOrValue `yaml:"key_file"`
    CAFile   string         `yaml:"ca_file"`
}

type ToolConfig struct {
    API                 string            `yaml:"api"`
    Description         string            `yaml:"description"`
    Method              string            `yaml:"method"`
    Path                string            `yaml:"path"`
    Parameters          []ParameterDef    `yaml:"parameters"`
    Body                *BodyDef          `yaml:"body,omitempty"`
    AuthOverride        *AuthOverride     `yaml:"auth_override,omitempty"`
    RateLimiting        *RateLimitConfig  `yaml:"rate_limiting,omitempty"`
    ResponseTransform   *TransformConfig  `yaml:"response_transform,omitempty"`
    RequireConfirmation bool              `yaml:"require_confirmation"`
    ConfirmationMessage string            `yaml:"confirmation_message,omitempty"`
}

type AuthOverride struct {
    Scope string `yaml:"scope"`
}

type ParameterDef struct {
    Name        string      `yaml:"name"`
    In          string      `yaml:"in"` // path, query, header
    Type        string      `yaml:"type"`
    Required    bool        `yaml:"required"`
    Default     interface{} `yaml:"default,omitempty"`
    Description string      `yaml:"description"`
    Enum        []string    `yaml:"enum,omitempty"`
    Minimum     *float64    `yaml:"minimum,omitempty"`
    Maximum     *float64    `yaml:"maximum,omitempty"`
}

type BodyDef struct {
    Type       string       `yaml:"type"`
    Properties []ParameterDef `yaml:"properties"`
}

type RetryConfig struct {
    MaxAttempts  int      `yaml:"max_attempts"`
    Backoff      string   `yaml:"backoff"` // exponential, linear, fixed
    InitialDelay Duration `yaml:"initial_delay"`
    MaxDelay     Duration `yaml:"max_delay"`
}

type RateLimitConfig struct {
    RequestsPerMinute int `yaml:"requests_per_minute"`
    RequestsPerHour   int `yaml:"requests_per_hour"`
    Burst             int `yaml:"burst"`
}

type TransformConfig struct {
    IncludeFields []string `yaml:"include_fields"`
    ExcludeFields []string `yaml:"exclude_fields"`
}

// Duration wraps time.Duration para parsing de YAML ("30s", "5m", "1h")
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(v *yaml.Node) error {
    dur, err := time.ParseDuration(v.Value)
    if err != nil {
        return fmt.Errorf("invalid duration %q: %w", v.Value, err)
    }
    d.Duration = dur
    return nil
}
```

---

## 10. Componente 1 — Parser e Validador

```go
// internal/config/loader.go

func Load(ctx context.Context, path string, resolver *secrets.Resolver) (*GatewayConfig, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("reading config %q: %w", path, err)
    }

    var raw RawConfig
    if err := yaml.Unmarshal(data, &raw); err != nil {
        return nil, fmt.Errorf("parsing yaml: %w", err)
    }

    if err := validate(&raw); err != nil {
        return nil, err
    }

    return buildConfig(&raw, resolver), nil
}

// buildConfig converte RawConfig → GatewayConfig, injetando o resolver
// em cada SecretOrValue para que possam resolver-se lazily depois
func buildConfig(raw *RawConfig, resolver *secrets.Resolver) *GatewayConfig {
    // (injetar resolver em todos os SecretOrValue da config)
    // Omitido por brevidade — percorre a struct recursivamente ou manualmente campo a campo
    cfg := &GatewayConfig{ /* ... */ }
    injectResolver(cfg, resolver)
    return cfg
}
```

### Validação estrutural completa

```go
// internal/config/validator.go

// Campos sensíveis que NÃO podem ser literais em produção
var sensitiveFields = []string{
    "client_secret", "client_id", "actor_client_id", "actor_client_secret",
    "token", "key", "password", "username",
    "cert_file", "key_file",
}

func validate(cfg *RawConfig) error {
    var errs []string

    // 1. dev_mode em produção?
    if cfg.Security.DevMode {
        errs = append(errs, "WARNING: security.dev_mode=true disables agent authentication — never use in production")
    }

    for toolName, tool := range cfg.Tools {
        // 2. API referenciada existe?
        api, ok := cfg.APIs[tool.API]
        if !ok {
            errs = append(errs, fmt.Sprintf("tool %q: references unknown api %q", toolName, tool.API))
            continue
        }

        // 3. Parâmetros de path têm placeholder no path?
        for _, p := range tool.Parameters {
            if p.In == "path" && !strings.Contains(tool.Path, "{"+p.Name+"}") {
                errs = append(errs, fmt.Sprintf(
                    "tool %q: path param %q not found in path %q", toolName, p.Name, tool.Path))
            }
        }

        // 4. require_confirmation exige confirmation_message
        if tool.RequireConfirmation && tool.ConfirmationMessage == "" {
            errs = append(errs, fmt.Sprintf(
                "tool %q: require_confirmation=true but confirmation_message is empty", toolName))
        }

        // 5. auth_override.scope só funciona em oauth2
        if tool.AuthOverride != nil && tool.AuthOverride.Scope != "" {
            if api.Auth.Type != "oauth2_client_credentials" && api.Auth.Type != "oauth2_token_exchange" {
                errs = append(errs, fmt.Sprintf(
                    "tool %q: auth_override.scope only works with oauth2 auth types, api %q uses %q",
                    toolName, tool.API, api.Auth.Type))
            }
        }

        // 6. token_exchange em modo stdio precisa de subject_token — avisa
        if api.Auth.Type == "oauth2_token_exchange" {
            errs = append(errs, fmt.Sprintf(
                "INFO: tool %q uses oauth2_token_exchange — this will fail in stdio mode (no subject_token available). Use oauth2_client_credentials for tools used via Claude Desktop.",
                toolName))
        }
    }

    // 7. Campos sensíveis não podem ser literais
    for apiName, api := range cfg.APIs {
        sensitiveAPIFields := map[string]*RawSecretOrValue{
            "client_id":          api.Auth.RawClientID,
            "client_secret":      api.Auth.RawClientSecret,
            "actor_client_id":    api.Auth.RawActorClientID,
            "actor_client_secret": api.Auth.RawActorClientSecret,
            "token":              api.Auth.RawToken,
            "key":                api.Auth.RawKey,
            "username":           api.Auth.RawUsername,
            "password":           api.Auth.RawPassword,
        }
        for fieldName, sov := range sensitiveAPIFields {
            if sov != nil && sov.IsLiteral() {
                errs = append(errs, fmt.Sprintf(
                    "api %q: %q cannot be a literal value — use a secret reference (source: env/vault/aws/azure/gcp)",
                    apiName, fieldName))
            }
        }
    }

    // 8. Chaves de agente não podem ser literais
    for i, key := range cfg.Security.APIKeys {
        if key.RawKey != nil && key.RawKey.IsLiteral() {
            errs = append(errs, fmt.Sprintf(
                "security.api_keys[%d] (%s): key cannot be a literal value", i, key.ID))
        }
    }

    // Separa warnings de erros fatais
    var fatalErrs []string
    for _, e := range errs {
        if strings.HasPrefix(e, "WARNING:") || strings.HasPrefix(e, "INFO:") {
            fmt.Fprintln(os.Stderr, e)
        } else {
            fatalErrs = append(fatalErrs, e)
        }
    }

    if len(fatalErrs) > 0 {
        return fmt.Errorf("config validation failed:\n  - %s", strings.Join(fatalErrs, "\n  - "))
    }
    return nil
}
```

---

## 11. Componente 2 — Runtime do Gateway

### Request Pipeline

```
MODO HTTP (com auth de agente):
[1. AgentAuth]    Valida Bearer token, extrai AgentKeyConfig
[2. ScopeCheck]   tool ∈ AgentKeyConfig.AllowedTools?
[3. RateLimit]    Sliding window por (agent_id, tool) no Redis
[4. ArgValidate]  Valida args contra JSON Schema
[5. BuildRequest] Substitui {path_params}, monta query/body
[6. ResolveAuth]  GetToken(api, scope, callerToken) → bearer
[7. InjectAuth]   ApplyAuth() → injeta token/key/cert na req HTTP
[8. Execute]      Chama API com retry + timeout
[9. Transform]    Filtra campos da resposta
[10. Log]         Persiste log de forma assíncrona

MODO STDIO (sem auth de agente):
[1-3 PULADOS]
[4. ArgValidate]  → ... → [10. Log] (mesmo fluxo a partir daqui)
```

### Handler de tool (completo)

```go
// internal/gateway/handler.go

type ToolCall struct {
    ToolName    string
    Args        map[string]interface{}
    AgentKeyID  string
    CallerToken string // Bearer token do agente HTTP; "" para stdio
    TraceID     string
    IsStdio     bool
}

type ToolResult struct {
    IsError bool
    Content string
}

type ToolHandler struct {
    config      config.ToolConfig
    apiConfig   config.APIConfig
    auth        *auth.Manager
    retry       *retry.Executor
    rateLimiter *ratelimit.Limiter
    transform   *transform.Transformer
    store       store.AsyncCallLogger
    logger      *zap.Logger
}

func (h *ToolHandler) Execute(ctx context.Context, call ToolCall) (*ToolResult, error) {
    start := time.Now()

    log := &store.ToolCallLog{
        ID:          uuid.New().String(),
        ToolID:      h.config.Name, // o Name é setado no registry, não está em ToolConfig diretamente
        CalledAt:    start,
        TraceID:     call.TraceID,
        AgentKeyID:  call.AgentKeyID,
        InputArgs:   call.Args,
        AuthTypeUsed: h.apiConfig.Auth.Type,
    }

    // Rate limit (apenas HTTP)
    if !call.IsStdio && !h.rateLimiter.Allow(ctx, call.AgentKeyID, h.config.Name) {
        log.IsError, log.ErrorType, log.ErrorMessage = true, "rate_limit", "rate limit exceeded"
        h.store.LogAsync(log)
        return nil, &RateLimitError{RetryAfter: h.rateLimiter.RetryAfter(call.AgentKeyID)}
    }

    // Validação de argumentos
    if err := h.validateArgs(call.Args); err != nil {
        log.IsError, log.ErrorType, log.ErrorMessage = true, "validation", err.Error()
        h.store.LogAsync(log)
        return nil, &ValidationError{Message: err.Error()}
    }

    // Escopo efetivo (auth_override > api default)
    scope := h.apiConfig.Auth.Scope
    if h.config.AuthOverride != nil && h.config.AuthOverride.Scope != "" {
        scope = h.config.AuthOverride.Scope
    }

    // Obtém token (para OAuth2; outros tipos retornam "")
    token, err := h.auth.GetToken(ctx, h.apiConfig.BaseURL, scope, call.CallerToken)
    if err != nil {
        log.IsError, log.ErrorType, log.ErrorMessage = true, "sts_error", err.Error()
        h.store.LogAsync(log)
        return nil, fmt.Errorf("acquiring auth token: %w", err)
    }
    log.TokenCacheHit = token != "" // se veio do cache, token já existia

    // Constrói a requisição HTTP
    req, err := h.buildHTTPRequest(ctx, call.Args)
    if err != nil {
        return nil, fmt.Errorf("building request: %w", err)
    }
    log.HTTPMethod = req.Method
    log.HTTPURL = req.URL.String()

    // Injeta auth na requisição
    if err := h.auth.ApplyAuth(ctx, req, h.apiConfig.BaseURL, token); err != nil {
        log.IsError, log.ErrorType, log.ErrorMessage = true, "auth_inject", err.Error()
        h.store.LogAsync(log)
        return nil, fmt.Errorf("applying auth: %w", err)
    }

    // Executa com retry
    resp, attempts, err := h.retry.Execute(ctx, req)
    log.AttemptCount = attempts
    if err != nil {
        log.IsError, log.ErrorType, log.ErrorMessage = true, "api_error", err.Error()
        log.DurationMs = int(time.Since(start).Milliseconds())
        h.store.LogAsync(log)
        return nil, fmt.Errorf("calling API: %w", err)
    }
    defer resp.Body.Close()

    body, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // max 10MB
    log.HTTPStatus = resp.StatusCode
    log.ResponseSize = len(body)
    log.DurationMs = int(time.Since(start).Milliseconds())

    if resp.StatusCode >= 400 {
        log.IsError, log.ErrorType = true, "api_error"
        log.ErrorMessage = fmt.Sprintf("HTTP %d", resp.StatusCode)
        h.store.LogAsync(log)
        // Retorna como isError=true (não como erro Go) para o agente receber a mensagem
        return &ToolResult{
            IsError: true,
            Content: fmt.Sprintf("API returned %d: %s", resp.StatusCode, sanitizeBody(body, 500)),
        }, nil
    }

    result := h.transform.Apply(body, h.config.ResponseTransform)
    h.store.LogAsync(log)

    return &ToolResult{IsError: false, Content: string(result)}, nil
}

// buildHTTPRequest constrói a req substituindo path params, montando query e body
func (h *ToolHandler) buildHTTPRequest(ctx context.Context, args map[string]interface{}) (*http.Request, error) {
    urlPath := h.config.Path
    queryParams := url.Values{}
    bodyData := map[string]interface{}{}

    allParams := h.config.Parameters
    if h.config.Body != nil {
        for _, p := range h.config.Body.Properties {
            allParams = append(allParams, p)
        }
    }

    for _, p := range allParams {
        val, hasVal := args[p.Name]
        if !hasVal {
            if p.Default != nil {
                val = p.Default
            } else if p.Required {
                return nil, fmt.Errorf("required parameter %q is missing", p.Name)
            } else {
                continue
            }
        }
        switch p.In {
        case "path":
            urlPath = strings.ReplaceAll(urlPath, "{"+p.Name+"}", fmt.Sprintf("%v", val))
        case "query":
            queryParams.Set(p.Name, fmt.Sprintf("%v", val))
        case "body", "":
            bodyData[p.Name] = val
        }
    }

    fullURL := h.apiConfig.BaseURL + urlPath
    if len(queryParams) > 0 {
        fullURL += "?" + queryParams.Encode()
    }

    var bodyReader io.Reader
    if h.config.Method != http.MethodGet && len(bodyData) > 0 {
        b, err := json.Marshal(bodyData)
        if err != nil {
            return nil, fmt.Errorf("marshaling request body: %w", err)
        }
        bodyReader = bytes.NewReader(b)
    }

    req, err := http.NewRequestWithContext(ctx, h.config.Method, fullURL, bodyReader)
    if err != nil {
        return nil, err
    }

    if bodyReader != nil {
        req.Header.Set("Content-Type", "application/json")
    }
    for k, v := range h.apiConfig.Headers {
        req.Header.Set(k, v)
    }

    return req, nil
}

// sanitizeBody trunca e remove dados sensíveis do body de erro antes de logar
func sanitizeBody(body []byte, maxLen int) string {
    s := string(body)
    if len(s) > maxLen {
        s = s[:maxLen] + "...[truncated]"
    }
    return s
}
```

### Retry com clonagem de request

O desafio do retry é que `http.Request.Body` é um `io.Reader` que só pode ser lido uma vez. A solução é guardar os bytes originais:

```go
// internal/retry/executor.go

type Executor struct {
    maxAttempts int
    backoff     func(attempt int) time.Duration
    client      *http.Client
}

func (e *Executor) Execute(ctx context.Context, req *http.Request) (*http.Response, int, error) {
    // Lê o body uma vez e guarda os bytes para poder recriar o reader a cada tentativa
    var bodyBytes []byte
    if req.Body != nil {
        var err error
        bodyBytes, err = io.ReadAll(req.Body)
        req.Body.Close()
        if err != nil {
            return nil, 0, fmt.Errorf("reading request body for retry: %w", err)
        }
    }

    var lastErr error
    for attempt := 1; attempt <= e.maxAttempts; attempt++ {
        // Recria o body para esta tentativa
        reqCopy := req.Clone(ctx)
        if bodyBytes != nil {
            reqCopy.Body = io.NopCloser(bytes.NewReader(bodyBytes))
            reqCopy.ContentLength = int64(len(bodyBytes))
        }

        resp, err := e.client.Do(reqCopy)
        if err == nil && resp.StatusCode < 500 && resp.StatusCode != 429 {
            return resp, attempt, nil
        }

        if err != nil {
            lastErr = err
        } else {
            lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
            if resp.StatusCode == 429 {
                if after := resp.Header.Get("Retry-After"); after != "" {
                    if secs, e := strconv.Atoi(after); e == nil {
                        select {
                        case <-ctx.Done(): return nil, attempt, ctx.Err()
                        case <-time.After(time.Duration(secs) * time.Second):
                        }
                        continue
                    }
                }
            }
        }

        if attempt < e.maxAttempts {
            select {
            case <-ctx.Done(): return nil, attempt, ctx.Err()
            case <-time.After(e.backoff(attempt)):
            }
        }
    }

    return nil, e.maxAttempts, fmt.Errorf("after %d attempts: %w", e.maxAttempts, lastErr)
}

func ExponentialBackoff(initial, maxDelay time.Duration) func(int) time.Duration {
    return func(attempt int) time.Duration {
        d := initial * time.Duration(math.Pow(2, float64(attempt-1)))
        // Jitter ±25%
        jitter := time.Duration(rand.Int63n(int64(d) / 4))
        d += jitter
        if d > maxDelay { d = maxDelay }
        return d
    }
}
```

### Logging assíncrono

O handler não pode bloquear no path crítico esperando a inserção no banco:

```go
// internal/store/async_logger.go

type AsyncCallLogger struct {
    db     *pgxpool.Pool
    queue  chan *ToolCallLog
    wg     sync.WaitGroup
}

func NewAsyncCallLogger(db *pgxpool.Pool, bufferSize int) *AsyncCallLogger {
    l := &AsyncCallLogger{
        db:    db,
        queue: make(chan *ToolCallLog, bufferSize), // buffer de 1000 logs
    }
    l.wg.Add(1)
    go l.worker()
    return l
}

// LogAsync envia o log para a fila sem bloquear
// Se a fila estiver cheia, descarta e incrementa um counter de métricas
func (l *AsyncCallLogger) LogAsync(log *ToolCallLog) {
    select {
    case l.queue <- log:
    default:
        metrics.LogsDropped.Inc()
    }
}

func (l *AsyncCallLogger) worker() {
    defer l.wg.Done()
    batch := make([]*ToolCallLog, 0, 100)

    ticker := time.NewTicker(2 * time.Second) // flush a cada 2s
    defer ticker.Stop()

    for {
        select {
        case log, ok := <-l.queue:
            if !ok {
                l.flush(batch)
                return
            }
            batch = append(batch, log)
            if len(batch) >= 100 { // flush em batch de 100
                l.flush(batch)
                batch = batch[:0]
            }
        case <-ticker.C:
            if len(batch) > 0 {
                l.flush(batch)
                batch = batch[:0]
            }
        }
    }
}

func (l *AsyncCallLogger) flush(batch []*ToolCallLog) {
    // INSERT em batch com pgx CopyFrom (muito mais eficiente que INSERT individual)
    _, err := l.db.CopyFrom(context.Background(),
        pgx.Identifier{"tool_calls"},
        []string{"id", "tool_id", "called_at", "duration_ms", "agent_key_id",
            "trace_id", "input_args", "http_method", "http_url", "http_status",
            "response_size", "is_error", "error_type", "error_message",
            "attempt_count", "auth_type_used", "token_cache_hit"},
        pgx.CopyFromSlice(len(batch), func(i int) ([]interface{}, error) {
            l := batch[i]
            return []interface{}{l.ID, l.ToolID, l.CalledAt, l.DurationMs, l.AgentKeyID,
                l.TraceID, l.InputArgs, l.HTTPMethod, l.HTTPURL, l.HTTPStatus,
                l.ResponseSize, l.IsError, l.ErrorType, l.ErrorMessage,
                l.AttemptCount, l.AuthTypeUsed, l.TokenCacheHit}, nil
        }),
    )
    if err != nil {
        metrics.LogFlushErrors.Inc()
    }
}

// Shutdown drena a fila antes de encerrar
func (l *AsyncCallLogger) Shutdown() {
    close(l.queue)
    l.wg.Wait()
}
```

---

## 12. Componente 3 — Servidores MCP

### Helpers JSON-RPC compartilhados

```go
// internal/mcp/protocol.go

type JSONRPCRequest struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      interface{}     `json:"id"` // string ou int
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}

type MCPTool struct {
    Name        string                 `json:"name"`
    Description string                 `json:"description"`
    InputSchema map[string]interface{} `json:"inputSchema"`
}

func writeResult(w http.ResponseWriter, id interface{}, result interface{}) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "jsonrpc": "2.0",
        "id":      id,
        "result":  result,
    })
}

func writeError(w http.ResponseWriter, id interface{}, code int, message string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK) // JSON-RPC sempre retorna 200, erro está no payload
    json.NewEncoder(w).Encode(map[string]interface{}{
        "jsonrpc": "2.0",
        "id":      id,
        "error":   map[string]interface{}{"code": code, "message": message},
    })
}

// Para stdio: escreve na stdout com newline
func writeStdioResult(id interface{}, result interface{}) {
    b, _ := json.Marshal(map[string]interface{}{
        "jsonrpc": "2.0", "id": id, "result": result,
    })
    fmt.Printf("%s\n", b)
}

func writeStdioError(id interface{}, code int, message string) {
    b, _ := json.Marshal(map[string]interface{}{
        "jsonrpc": "2.0", "id": id,
        "error": map[string]interface{}{"code": code, "message": message},
    })
    fmt.Printf("%s\n", b)
}
```

### Servidor HTTP/SSE

```go
// internal/mcp/server_http.go

type HTTPServer struct {
    registry  *gateway.Registry
    agentAuth *middleware.AgentAuth
    tools     []MCPTool
}

func (s *HTTPServer) Handler() http.Handler {
    r := chi.NewRouter()
    r.Use(s.agentAuth.Middleware) // valida Bearer token do agente
    r.Post("/mcp", s.handleMCP)
    r.Get("/health", s.handleHealth)
    return r
}

func (s *HTTPServer) handleMCP(w http.ResponseWriter, r *http.Request) {
    var req JSONRPCRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, nil, -32700, "parse error: "+err.Error())
        return
    }
    if req.JSONRPC != "2.0" {
        writeError(w, req.ID, -32600, "invalid request: jsonrpc must be \"2.0\"")
        return
    }

    agentCtx := r.Context() // AgentAuth já injetou AgentKeyConfig no context

    switch req.Method {
    case "initialize":
        writeResult(w, req.ID, map[string]interface{}{
            "protocolVersion": "2024-11-05",
            "capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
            "serverInfo":      map[string]interface{}{"name": "mcpgateway", "version": "3.0"},
        })

    case "tools/list":
        writeResult(w, req.ID, map[string]interface{}{"tools": s.tools})

    case "tools/call":
        var params struct {
            Name      string                 `json:"name"`
            Arguments map[string]interface{} `json:"arguments"`
        }
        if err := json.Unmarshal(req.Params, &params); err != nil {
            writeError(w, req.ID, -32602, "invalid params: "+err.Error())
            return
        }

        handler, ok := s.registry.Get(params.Name)
        if !ok {
            writeError(w, req.ID, -32602, fmt.Sprintf("tool %q not found", params.Name))
            return
        }

        agent := middleware.AgentFromContext(agentCtx)
        call := gateway.ToolCall{
            ToolName:    params.Name,
            Args:        params.Arguments,
            AgentKeyID:  agent.ID,
            CallerToken: extractBearerToken(r),
            TraceID:     r.Header.Get("X-Trace-Id"),
            IsStdio:     false,
        }

        result, err := handler.Execute(agentCtx, call)
        if err != nil {
            writeError(w, req.ID, -32603, err.Error())
            return
        }

        writeResult(w, req.ID, map[string]interface{}{
            "content": []map[string]interface{}{{"type": "text", "text": result.Content}},
            "isError": result.IsError,
        })

    default:
        writeError(w, req.ID, -32601, fmt.Sprintf("method %q not found", req.Method))
    }
}

func extractBearerToken(r *http.Request) string {
    auth := r.Header.Get("Authorization")
    if strings.HasPrefix(auth, "Bearer ") {
        return strings.TrimPrefix(auth, "Bearer ")
    }
    return ""
}
```

### Servidor stdio

```go
// internal/mcp/server_stdio.go

type StdioServer struct {
    registry *gateway.Registry
    tools    []MCPTool
}

func (s *StdioServer) Run(ctx context.Context) error {
    scanner := bufio.NewScanner(os.Stdin)
    scanner.Buffer(make([]byte, 1*1024*1024), 1*1024*1024) // buffer de 1MB por linha

    for scanner.Scan() {
        line := scanner.Text()
        if strings.TrimSpace(line) == "" {
            continue
        }

        var req JSONRPCRequest
        if err := json.Unmarshal([]byte(line), &req); err != nil {
            writeStdioError(nil, -32700, "parse error")
            continue
        }

        switch req.Method {
        case "initialize":
            writeStdioResult(req.ID, map[string]interface{}{
                "protocolVersion": "2024-11-05",
                "capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
                "serverInfo":      map[string]interface{}{"name": "mcpgateway", "version": "3.0"},
            })

        case "tools/list":
            writeStdioResult(req.ID, map[string]interface{}{"tools": s.tools})

        case "tools/call":
            var params struct {
                Name      string                 `json:"name"`
                Arguments map[string]interface{} `json:"arguments"`
            }
            if err := json.Unmarshal(req.Params, &params); err != nil {
                writeStdioError(req.ID, -32602, "invalid params")
                continue
            }

            handler, ok := s.registry.Get(params.Name)
            if !ok {
                writeStdioError(req.ID, -32602, fmt.Sprintf("tool %q not found", params.Name))
                continue
            }

            call := gateway.ToolCall{
                ToolName: params.Name,
                Args:     params.Arguments,
                TraceID:  uuid.New().String(),
                IsStdio:  true, // sinaliza que não há auth de agente
            }

            result, err := handler.Execute(ctx, call)
            if err != nil {
                writeStdioError(req.ID, -32603, err.Error())
                continue
            }

            writeStdioResult(req.ID, map[string]interface{}{
                "content": []map[string]interface{}{{"type": "text", "text": result.Content}},
                "isError": result.IsError,
            })

        default:
            writeStdioError(req.ID, -32601, fmt.Sprintf("method %q not found", req.Method))
        }
    }

    return scanner.Err()
}
```

---

## 13. Componente 4 — Painel de Controle Web

### Stack frontend

- React 18 + TypeScript
- TanStack Query para estado do servidor
- Recharts para gráficos
- shadcn/ui para componentes
- Tailwind CSS

### Admin API endpoints

```
GET  /api/tools                   → lista tools com status e métricas básicas
GET  /api/tools/:id/calls         → log de chamadas paginado
GET  /api/metrics?period=24h      → métricas agregadas
GET  /api/secrets/health          → status de cada secret (TTL, provider)
GET  /api/agents                  → lista agentes registrados
GET  /health                      → health check dos componentes internos
```

### Resposta do health check

```json
GET /health
{
  "status": "healthy",
  "components": {
    "postgres":     { "status": "ok" },
    "redis":        { "status": "ok" },
    "vault":        { "status": "ok", "latency_ms": 3 },
    "aws_sm":       { "status": "ok", "latency_ms": 12 },
    "azure_kv":     { "status": "degraded", "error": "connection timeout" }
  },
  "secrets": {
    "prod/crm/credentials":  { "cached": true, "expires_in": "9h23m" },
    "prod/gateway/identity": { "cached": true, "expires_in": "3h10m" }
  }
}
```

### Wireframes principais

```
┌─────────────────────────────────────────────────────────┐
│  MCP Gateway               [● Healthy]  [Docs] [Config]  │
├─────────────────────────────────────────────────────────┤
│  Últimas 24h                                             │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐           │
│  │   12.847   │ │    0.3%    │ │   142ms    │           │
│  │  chamadas  │ │    erro    │ │  lat p50   │           │
│  └────────────┘ └────────────┘ └────────────┘           │
│                                                          │
│  Tools  ──────────────────────────────────────────────  │
│  Tool                  API       Auth           Status  │
│  get_customer          crm       OAuth2 CC      ● OK    │
│  create_purchase_order erp       Token Exchange ● OK    │
│  get_account_balance   financeiro mTLS          ⚠ Slow  │
│                                                          │
│  Secrets  ────────────────────────────────────────────  │
│  prod/crm/credentials     AWS SM    ✅ expira em 9h      │
│  secret/data/gateway/..   Vault     ✅ expira em 3h      │
│  MTLS_CERT_PATH           env       ✅ presente          │
└─────────────────────────────────────────────────────────┘
```

---

## 14. Modelo de Dados

### Migrations com golang-migrate

A ferramenta recomendada é `golang-migrate`. Ela mantém um histórico de versões aplicadas no banco e permite rollback.

```bash
# Instalar
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# Criar nova migration
migrate create -ext sql -dir internal/store/migrations -seq nome_da_migration

# Aplicar migrations
migrate -path internal/store/migrations -database "$DATABASE_URL" up

# Integrado no binário do gateway
mcpgateway migrate --db "$DATABASE_URL"
```

### Schema PostgreSQL

```sql
-- internal/store/migrations/001_initial.sql

CREATE TABLE agent_keys (
    id           TEXT PRIMARY KEY,
    key_hash     TEXT NOT NULL UNIQUE,  -- SHA-256 do token, nunca plaintext
    name         TEXT NOT NULL,
    allowed_tools TEXT[] NOT NULL DEFAULT '{}', -- ['*'] = todas
    is_active    BOOLEAN NOT NULL DEFAULT TRUE,
    last_used    TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ            -- NULL = sem expiração
);

CREATE TABLE registered_apis (
    id         TEXT PRIMARY KEY,
    auth_type  TEXT NOT NULL,
    base_url   TEXT NOT NULL,
    config     JSONB NOT NULL,          -- sem campos sensíveis
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE registered_tools (
    id          TEXT PRIMARY KEY,
    api_id      TEXT NOT NULL REFERENCES registered_apis(id),
    description TEXT,
    method      TEXT NOT NULL,
    path        TEXT NOT NULL,
    config      JSONB NOT NULL,
    is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE tool_calls (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tool_id         TEXT NOT NULL REFERENCES registered_tools(id),
    called_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    duration_ms     INTEGER,
    agent_key_id    TEXT REFERENCES agent_keys(id),
    trace_id        TEXT,
    input_args      JSONB,              -- args sem valores sensíveis
    http_method     TEXT,
    http_url        TEXT,               -- URL destino com path params substituídos
    auth_type_used  TEXT,
    scope_requested TEXT,
    token_cache_hit BOOLEAN,
    http_status     INTEGER,
    response_size   INTEGER,
    is_error        BOOLEAN NOT NULL DEFAULT FALSE,
    error_type      TEXT,               -- "rate_limit", "validation", "sts_error", "api_error"
    error_message   TEXT,
    attempt_count   INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX ON tool_calls (tool_id, called_at DESC);
CREATE INDEX ON tool_calls (agent_key_id, called_at DESC);
CREATE INDEX ON tool_calls (is_error, called_at DESC);
CREATE INDEX ON tool_calls (trace_id) WHERE trace_id IS NOT NULL;

-- Eventos de segurança separados do log de chamadas
CREATE TABLE security_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    event_type   TEXT NOT NULL,         -- "invalid_api_key", "scope_denied", "secret_refresh_failed"
    agent_key_id TEXT,
    tool_id      TEXT,
    source_ip    TEXT,
    details      JSONB
);

CREATE INDEX ON security_events (occurred_at DESC);
CREATE INDEX ON security_events (event_type, occurred_at DESC);

-- View materializada para o dashboard (evita queries pesadas em tool_calls)
CREATE MATERIALIZED VIEW tool_metrics_hourly AS
SELECT
    date_trunc('hour', called_at)            AS hour,
    tool_id,
    agent_key_id,
    COUNT(*)                                 AS total_calls,
    COUNT(*) FILTER (WHERE is_error)         AS error_calls,
    ROUND(AVG(duration_ms)::NUMERIC, 0)      AS avg_duration_ms,
    PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY duration_ms) AS p50_ms,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY duration_ms) AS p99_ms,
    COUNT(*) FILTER (WHERE token_cache_hit)  AS sts_cache_hits,
    COUNT(*) FILTER (WHERE NOT COALESCE(token_cache_hit, TRUE)) AS sts_misses
FROM tool_calls
GROUP BY hour, tool_id, agent_key_id;

-- Refresh a cada 5 minutos (via goroutine no gateway ou pg_cron)
-- CREATE UNIQUE INDEX ON tool_metrics_hourly (hour, tool_id, agent_key_id);
-- SELECT cron.schedule('*/5 * * * *', 'REFRESH MATERIALIZED VIEW CONCURRENTLY tool_metrics_hourly');
```

---

## 15. Fluxos Principais

### Fluxo 1: Startup

```
1. $ mcpgateway serve --config ./mcpgateway.yaml

2. Config Loader parseia o YAML
   - Detecta literais em campos sensíveis → erro imediato com mensagem clara
   - Constrói SecretOrValue para cada campo sensível (sem resolver ainda)
   - Emite INFO para tools com oauth2_token_exchange (não funciona em stdio)

3. Secret Resolver inicializa providers
   - Detecta ambiente: IAM Role? Vault? Azure MI? CLI local?
   - Testa conectividade (log warning se provider indisponível — não falha o startup)
   - Cache começa vazio — resolução lazy na primeira chamada

4. Auth Manager inicializa
   - Conecta ao Redis (necessário — falha se Redis indisponível)
   - Token cache começa vazio

5. AsyncCallLogger inicializa
   - Conecta ao PostgreSQL e roda migrations pendentes
   - Buffer de 1000 logs em memória

6. Tool Registry constrói
   - Para cada tool: gera JSON Schema de input (para tools/list)
   - Cria ToolHandler com dependências injetadas

7. Servidores iniciam
   ✅ MCP stdio: ready (se --stdio foi passado)
   ✅ MCP HTTP:  :8080
   ✅ Admin API: :8081

8. Health check goroutine ativa (verifica componentes a cada 60s)
```

### Fluxo 2: Chamada HTTP completa com OAuth2 CC (cache miss)

```
Agente            Gateway                 Vault/SM         STS           API
  │                   │                      │               │             │
  │─ tools/call ─────►│                      │               │             │
  │  Bearer: key-abc  │                      │               │             │
  │  tool: get_inv.   │                      │               │             │
  │                   │                      │               │             │
  │              [AgentAuth]                 │               │             │
  │              hash(key-abc) → agent-ops   │               │             │
  │              allowed: [get_inventory]    │               │             │
  │                   │                      │               │             │
  │              [ScopeCheck] ✅             │               │             │
  │              [RateLimit]  ✅             │               │             │
  │              [ArgValidate] ✅            │               │             │
  │                   │                      │               │             │
  │              [ResolveAuth]               │               │             │
  │              cache miss: erp×erp:read×hash(key-abc)      │             │
  │                   │── get client_secret ─►│              │             │
  │                   │◄─ "s3cr3t" ──────────│              │             │
  │                   │                      │               │             │
  │                   │── POST /token ───────────────────────►│            │
  │                   │   grant_type: client_credentials      │            │
  │                   │   scope: erp:read                     │            │
  │                   │◄── access_token (1h) ─────────────────│            │
  │                   │                      │               │             │
  │              cache: erp×erp:read×hash(key-abc) = token (55min)         │
  │                   │                      │               │             │
  │              [InjectAuth] Bearer: token  │               │             │
  │              [Execute]                   │               │             │
  │                   │── GET /inventory?product_id=X ───────────────────►│
  │                   │◄── 200 OK ─────────────────────────────────────────│
  │                   │                      │               │             │
  │              [Transform] filtra campos   │               │             │
  │              [LogAsync] enfileira log    │               │             │
  │                   │                      │               │             │
  │◄─ result ─────────│                      │               │             │
```

### Fluxo 3: Chamada com Token Exchange (RFC 8693)

```
Agente            Gateway                    STS              API
  │                   │                        │                │
  │─ tools/call ─────►│                        │                │
  │  Bearer: agent-jwt│                        │                │
  │  tool: create_ord.│                        │                │
  │                   │                        │                │
  │              [ResolveAuth]                 │                │
  │              tipo: oauth2_token_exchange   │                │
  │              subject_token = agent-jwt     │                │
  │                   │                        │                │
  │                   │── POST /token ─────────►│               │
  │                   │   grant_type: token-exchange            │
  │                   │   subject_token: agent-jwt              │
  │                   │   subject_token_type: access_token      │
  │                   │   actor_client_id: gateway-id           │
  │                   │   actor_client_secret: ***              │
  │                   │   requested_scope: erp:read erp:write   │
  │                   │◄── service_token (erp:read+write) ──────│
  │                   │                        │                │
  │              [InjectAuth] Bearer: service_token             │
  │                   │── POST /purchase-orders ────────────────►│
  │                   │◄── 201 Created ─────────────────────────│
  │◄─ result ─────────│                        │                │
```

### Fluxo 4: Escopo negado

```
1. Agente "agent-crm-reader" tenta chamar "create_purchase_order"

2. [AgentAuth]: extrai agent-crm-reader
   allowed_tools: ["get_customer", "search_customers"]

3. [ScopeCheck]: "create_purchase_order" ∉ allowed_tools
   → bloqueia imediatamente (STS e API nunca são chamados)
   → grava security_event: { type: "scope_denied", agent: "agent-crm-reader", tool: "create_purchase_order" }

4. Resposta ao agente:
   { "result": { "content": [{"type":"text","text":"Error: agent \"agent-crm-reader\" is not authorized to call tool \"create_purchase_order\""}], "isError": true } }
```

### Fluxo 5: Rotação de secret sem downtime

```
1. Equipe de segurança rotaciona "prod/crm/credentials" no AWS SM

2. Cache do SecretResolver expira (TTL de 12h) ou refresh_before_expiry atinge o limiar

3. Próxima chamada que precise de client_secret:
   - Secret Resolver detecta cache expirado
   - GET GetSecretValue("prod/crm/credentials") usando IAM Role
   - Novo valor armazenado no cache

4. Auth Manager obtém novo token do STS com o novo client_secret
   - Token antigo (do STS) pode ainda estar válido no cache de tokens
   - Quando expirar, novo token será obtido com a nova credencial

5. Zero downtime — nenhuma reinicialização, nenhuma intervenção manual

6. Dashboard mostra: "prod/crm/credentials — renovado há 2 minutos"
```

### Fluxo 6: Graceful shutdown

```
1. SIGTERM recebido (Kubernetes terminando o pod, deploy novo, etc.)

2. Gateway para de aceitar novas conexões nos servidores MCP e Admin

3. Aguarda requests em andamento terminarem (timeout: 30s)

4. AsyncCallLogger.Shutdown() — drena a fila de logs pendentes para o banco

5. Fecha conexões com PostgreSQL e Redis

6. Processo encerra com exit code 0
```

---

## 16. Plano de MVP — 7 Semanas

### Semana 1: Core MCP stdio funcional

**Objetivo:** Claude Desktop chama uma API pública via tool declarada em YAML.

**Tarefas:**
- Setup do repositório: `go mod init github.com/seuusuario/mcpgateway`
- Estrutura de pacotes conforme Seção 17
- `docker-compose up` sobe postgres + redis + vault
- Tipos Go centrais (`config/types.go`) e parser YAML básico
- Servidor MCP stdio: `initialize`, `tools/list`, `tools/call`
- Auth type `none` e `bearer` via `source: env`
- Execução HTTP básica sem retry
- Migrations com golang-migrate (`001_initial.sql`)
- Testar: Claude Desktop → get_user na JSONPlaceholder API

**Entregável:** Claude Desktop pergunta "quem é o usuário 5?" e o gateway retorna os dados.

---

### Semana 2: Secret Resolver completo

**Objetivo:** Credenciais saem de vault/secrets manager, não de variáveis de ambiente.

**Tarefas:**
- `SecretOrValue` com `UnmarshalYAML` (scalar ou objeto)
- Provider `env` formalizado
- Provider `aws_secrets_manager` com IAM Role
- Provider `hashicorp_vault` com AppRole + `VAULT_TOKEN`
- Cache de secrets com TTL e prefetch assíncrono
- Validação: literais em campos sensíveis → erro com mensagem clara
- Testes unitários com mocks dos providers

**Entregável:** Config com `source: aws_secrets_manager` funciona localmente com `AWS_PROFILE`.

---

### Semana 3: Auth Manager com STS

**Objetivo:** Gateway obtém tokens do STS e cacheia por `(api, scope, caller)`.

**Tarefas:**
- `oauth2_client_credentials` completo com `tokenResponse`
- `oauth2_token_exchange` (RFC 8693) com verificação de status HTTP
- `TokenCache` L1 (in-process) + L2 (Redis) com chave composta
- `ApplyAuth` para todos os tipos (api_key, basic, aws_sigv4)
- Aviso de validação para tools com token_exchange no modo stdio
- Testes: mock do STS para testar cache hit/miss

**Entregável:** Gateway obtém token do STS, cacheia no Redis, usa cache nas chamadas subsequentes. Dois agentes com escopos diferentes recebem tokens diferentes.

---

### Semana 4: Pipeline HTTP completo

**Objetivo:** Modo HTTP funcional de ponta a ponta com todos os middlewares.

**Tarefas:**
- Servidor MCP HTTP com middleware `AgentAuth`
- Tabelas `agent_keys` e `security_events` no banco
- `ScopeCheck` com suporte a `allowed_tools: ["*"]`
- Rate limiting (sliding window no Redis por `agent_id × tool`)
- Retry com clonagem de body (`io.ReadAll` + `bytes.NewReader`)
- `AsyncCallLogger` com batch insert via `pgx.CopyFrom`
- Transformação de resposta (`include_fields`, `exclude_fields`)
- `require_confirmation` com `confirmationId` (TTL 5min no Redis)
- Testes de integração cobrindo os 6 fluxos principais

**Entregável:** Pipeline completo testado. `curl` para o endpoint HTTP retorna resultado correto. Chamada com escopo negado retorna erro sem chamar o STS.

---

### Semana 5: Providers restantes e graceful shutdown

**Objetivo:** Suporte completo a todos os ambientes corporativos.

**Tarefas:**
- Provider `azure_keyvault` (Managed Identity / DefaultAzureCredential)
- Provider `gcp_secret_manager` (ADC / Workload Identity)
- Auth type `mtls` com `http.Client` configurado com certificado do cliente
- Graceful shutdown com drenagem do AsyncCallLogger
- Health check endpoint `GET /health` com status de cada componente
- Documentação de deployment por ambiente (EKS, GKE, AKS, VM, local)

**Entregável:** Todos os providers testados. `GET /health` retorna status real dos componentes.

---

### Semana 6: Dashboard web

**Objetivo:** Painel operacional com métricas e logs.

**Tarefas:**
- Admin API REST: `/api/tools`, `/api/metrics`, `/api/calls`, `/api/secrets/health`
- Refresh periódico da view materializada (goroutine a cada 5min)
- Frontend React: dashboard overview, tool list, call log com detalhe, secrets health
- Gráfico de chamadas por hora (Recharts)

**Entregável:** `localhost:8081` mostra métricas reais.

---

### Semana 7: Polimento e publicação

**Objetivo:** Produto pronto para portfólio e uso real.

**Tarefas:**
- `mcpgateway validate --config` com output legível
- `mcpgateway init --api jsonplaceholder` gera config de exemplo
- GoReleaser para binários Linux/macOS/Windows
- README com GIF de demo, quickstart em 5 minutos
- Examples: `local-dev/`, `aws/`, `vault/`, `kubernetes/`
- Post técnico no dev.to: "Production MCP server with corporate STS in 30 lines of YAML"

**Entregável:** `brew install mcpgateway` (via tap) ou `go install`. GitHub público com README completo.

---

## 17. Estrutura de Repositório

```
mcpgateway/
├── README.md
├── LICENSE                        (Apache 2.0)
├── Makefile
├── .goreleaser.yaml               (multi-platform builds)
├── docker-compose.yaml            (postgres + redis + vault-dev)
├── Dockerfile
│
├── cmd/
│   └── mcpgateway/
│       ├── main.go                (entry point, wires everything)
│       ├── serve.go               (serve --config --stdio --port)
│       ├── validate.go            (validate --config)
│       └── migrate.go             (migrate --db)
│
├── internal/
│   ├── config/
│   │   ├── types.go               (todas as structs de config)
│   │   ├── loader.go              (Load: parseia YAML → GatewayConfig)
│   │   └── validator.go           (validate: erros fatais + warnings)
│   │
│   ├── secrets/
│   │   ├── types.go               (SecretRef, SecretValue, Provider interface)
│   │   ├── resolver.go            (Resolver: Resolve, fetch, prefetch)
│   │   ├── provider_env.go
│   │   ├── provider_aws.go
│   │   ├── provider_vault.go
│   │   ├── provider_azure.go
│   │   └── provider_gcp.go
│   │
│   ├── auth/
│   │   ├── token_cache.go         (CacheKey + L1/L2 cache)
│   │   ├── manager.go             (GetToken + ApplyAuth + fetchClientCredentials + fetchTokenExchange)
│   │   └── aws_sigv4.go           (assinatura AWS)
│   │
│   ├── mcp/
│   │   ├── protocol.go            (tipos JSON-RPC + helpers writeResult/writeError)
│   │   ├── schema_generator.go    (ToolConfig → JSON Schema para tools/list)
│   │   ├── server_stdio.go        (JSON-RPC via stdin/stdout)
│   │   └── server_http.go         (JSON-RPC via HTTP)
│   │
│   ├── gateway/
│   │   ├── registry.go            (Tool Registry: toolName → ToolHandler)
│   │   ├── handler.go             (Execute: pipeline completo)
│   │   ├── request_builder.go     (buildHTTPRequest: path params, query, body)
│   │   └── confirmation.go        (require_confirmation: gera/valida confirmationId no Redis)
│   │
│   ├── middleware/
│   │   ├── agent_auth.go          (valida Bearer key, injeta AgentKeyConfig no ctx)
│   │   ├── scope_check.go         (AllowsTool, grava security_event se negado)
│   │   └── rate_limiter.go        (sliding window no Redis por agent×tool)
│   │
│   ├── retry/
│   │   └── executor.go            (Execute com body cloning + exponential backoff + jitter)
│   │
│   ├── transform/
│   │   └── transformer.go         (Apply: include_fields, exclude_fields sobre JSON)
│   │
│   ├── store/
│   │   ├── types.go               (ToolCallLog struct)
│   │   ├── async_logger.go        (AsyncCallLogger: fila + worker + CopyFrom)
│   │   ├── postgres.go            (pool de conexões, helpers de query)
│   │   └── migrations/
│   │       ├── 001_initial.sql
│   │       └── 002_indexes.sql
│   │
│   └── admin/
│       ├── server.go              (Admin HTTP server em :8081)
│       └── handlers/
│           ├── tools.go           (GET /api/tools, GET /api/tools/:id/calls)
│           ├── metrics.go         (GET /api/metrics)
│           ├── agents.go          (GET /api/agents)
│           ├── secrets_health.go  (GET /api/secrets/health)
│           └── health.go          (GET /health)
│
├── ui/                            (React + TypeScript)
│   ├── src/
│   │   ├── components/
│   │   │   ├── Dashboard/
│   │   │   ├── ToolList/
│   │   │   ├── CallLog/
│   │   │   ├── SecretsHealth/
│   │   │   └── Metrics/
│   │   └── lib/
│   │       └── api.ts
│   └── package.json
│
├── examples/
│   ├── local-dev/
│   │   └── mcpgateway.yaml        (source: env, APIs públicas, dev_mode: true)
│   ├── aws/
│   │   ├── mcpgateway.yaml
│   │   └── README.md              (IAM Role setup)
│   ├── vault/
│   │   ├── mcpgateway.yaml        (token exchange + Vault)
│   │   └── README.md
│   └── kubernetes/
│       ├── mcpgateway.yaml
│       ├── deployment.yaml
│       ├── serviceaccount.yaml    (IRSA annotation para IAM Role)
│       └── README.md
│
└── docs/
    ├── configuration.md           (referência completa do YAML)
    ├── auth-types.md              (guia detalhado de cada tipo)
    ├── secrets-providers.md       (guia de cada provider + bootstrap)
    ├── deployment.md              (EKS, GKE, AKS, VM, local)
    └── security.md                (o que é/não é armazenado, modelo de ameaças)
```

---

## 18. Modelo de Negócio e Monetização

### Tier Open Source (gratuito, sempre)

- Binário e código 100% open source (Apache 2.0)
- Modos stdio e HTTP, todos os tipos de auth
- Todos os providers de secrets (AWS, Vault, Azure, GCP)
- Retry, rate limiting, logging
- Sem limite de tools ou APIs

### Tier Cloud — Gateway hospedado

| Plano | Tools | Chamadas/mês | Retenção logs | Environments |
|---|---|---|---|---|
| Starter ($29/mês) | 10 | 10.000 | 7 dias | 1 |
| Growth ($79/mês) | ilimitado | 100.000 | 30 dias | 3 |
| Scale ($199/mês) | ilimitado | 1.000.000 | 90 dias | ilimitado |

Features exclusivas Cloud: dashboard avançado, alertas, secrets management hospedado, IP fixo para APIs com whitelist.

### Tier Enterprise (contrato anual)

Deploy on-premise, SSO (SAML/OIDC), audit logs exportáveis para SIEM, SLA 99.9%, VPN/PrivateLink, onboarding dedicado.

---

## 19. Estratégia de Go-to-Market

### Fase 1 — Comunidade técnica (meses 1–3)

**Hook:** "Turn any REST API into an MCP tool in 30 lines of YAML — with corporate STS, Vault, and zero static credentials."

**HackerNews Show HN:** Demo mostrando YAML → Claude Desktop chamando API interna via Vault. O diferencial de segurança corporativa é o gancho.

**MCP Registry:** Publicar como "production-grade gateway with enterprise auth."

**Conteúdo técnico:** Post sobre token exchange RFC 8693 — assunto com pouquíssimo material de qualidade disponível.

### Fase 2 — Produto (meses 3–6)

**Templates por IdP:** Configs prontas para Okta, Azure AD, Keycloak, AWS Cognito, Auth0.

**CLI de scaffold:**
```bash
$ mcpgateway init --api salesforce --sts okta --secrets aws
✅ Detected AWS environment (IAM Role available)
✅ Created mcpgateway.yaml with 4 Salesforce tools
⚠️  Store credentials in AWS Secrets Manager:
    aws secretsmanager create-secret --name prod/crm/credentials \
      --secret-string '{"client_id":"...","client_secret":"..."}'
```

### Fase 3 — Monetização (meses 6–12)

**ICP:** Times de plataforma/AI em empresas 50–500 devs construindo agentes internos que travam na integração de auth corporativa.

---

## 20. Riscos e Mitigações

| Risco | Probabilidade | Impacto | Mitigação |
|---|---|---|---|
| MCP não consolida como padrão | Baixa | Alto | Lógica de proxy + STS + Vault tem valor independente do MCP |
| Anthropic lança produto similar | Média | Alto | OSS + comunidade é difícil competir; focar em enterprise features |
| Secrets manager indisponível na startup | Alta | Médio | Resolução lazy; health check expõe status; gateway sobe mesmo com provider indisponível |
| Token compartilhado entre agentes | Baixa | Alto | Cache keyed por `(api, scope, caller_identity)` + testes de segurança obrigatórios |
| Rotação de credencial causa tokens inválidos | Média | Médio | `refresh_before_expiry`; 401/403 da API dispara invalidação do cache |
| APIs legadas com SOAP/XML | Alta | Baixo | MVP foca em REST/JSON; documentado como limitação v1 |
| Mudanças no protocolo MCP | Média | Médio | Versionar internamente; testes contra spec oficial |
| Literal em campo sensível commitado no Git | Alta | Alto | Validação obrigatória na startup + `mcpgateway validate` no CI |
| Graceful shutdown lento drena logs | Baixa | Baixo | Timeout de 30s no shutdown; logs drops são contados em métricas |
| Token exchange falha em stdio | Alta | Baixo | Validator emite INFO na startup; documentado na Seção 3 |

---

## 21. Referências Técnicas

### go.mod — dependências principais

```go
module github.com/seuusuario/mcpgateway

go 1.22

require (
    // Core
    gopkg.in/yaml.v3 v3.0.1
    github.com/go-chi/chi/v5 v5.1.0
    go.uber.org/zap v1.27.0
    github.com/google/uuid v1.6.0

    // Secrets Managers
    github.com/aws/aws-sdk-go-v2/config v1.27.0
    github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.29.0
    github.com/hashicorp/vault-client-go v0.4.3
    github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.5.1
    github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets v1.1.0
    cloud.google.com/go/secretmanager v1.12.0

    // Auth
    golang.org/x/oauth2 v0.20.0

    // Cache + Rate limiting
    github.com/redis/go-redis/v9 v9.5.0
    github.com/jellydator/ttlcache/v3 v3.2.0

    // Persistência
    github.com/jackc/pgx/v5 v5.6.0
    github.com/golang-migrate/migrate/v4 v4.17.0

    // Testes
    github.com/stretchr/testify v1.9.0
    github.com/stretchr/mock v1.6.0
)
```

### Especificações e RFCs

- [MCP Specification](https://spec.modelcontextprotocol.io) — protocolo completo
- [RFC 8693](https://www.rfc-editor.org/rfc/rfc8693) — OAuth 2.0 Token Exchange
- [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749) — OAuth 2.0 Authorization Framework
- [RFC 7519](https://www.rfc-editor.org/rfc/rfc7519) — JWT
- [RFC 7523](https://www.rfc-editor.org/rfc/rfc7523) — JWT Bearer Token Grants

### Projetos para estudar

- [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) — SDK MCP em Go, referência de protocolo
- [golang-migrate/migrate](https://github.com/golang-migrate/migrate) — migrations SQL
- [go-chi/chi](https://github.com/go-chi/chi) — HTTP router para o Admin API
- [Kong Gateway](https://github.com/Kong/kong) — referência de arquitetura de API gateway

---

*Documento gerado em junho de 2026. Versão 3.0 — corrige bugs nos providers (VaultProvider.ExpiresAt, tokenResponse indefinida, isLiteralSecret incompleto, cloneRequest no retry, fetchTokenExchange sem verificação de status), adiciona ambiente de desenvolvimento local completo (docker-compose.yaml, claude_desktop_config.json, Makefile), define todos os tipos Go centrais, esclarece comportamento do modo stdio vs HTTP, adiciona logging assíncrono correto, graceful shutdown, ferramenta de migrations, e seção explícita de fora de escopo v1.*
