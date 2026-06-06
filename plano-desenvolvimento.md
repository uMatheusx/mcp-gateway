# Plano de Desenvolvimento — MCP Gateway

## Ordem de construção

A ordem segue o grafo de dependências: cada camada só pode ser construída depois que as camadas abaixo dela estão prontas.

```
[1] internal/secrets/     ← sem dependências internas
[2] internal/config/      ← depende de secrets
[3] internal/store/       ← depende só de pgx (paralelo com config)
[4] internal/retry/       ← utilitário standalone (paralelo)
    internal/transform/   ← utilitário standalone (paralelo)
[5] internal/auth/        ← depende de secrets + config + Redis
[6] internal/gateway/     ← depende de tudo acima
[7] internal/middleware/  ← depende de config + Redis
[8] internal/mcp/         ← depende de gateway + middleware
[9] internal/admin/       ← depende de store
[10] ui/                  ← depende do admin API
```

---

## Semana 1 — Core MCP stdio funcional

**Objetivo:** Claude Desktop chama uma API pública via tool declarada em YAML.

### O que construir

1. Setup do repositório: `go mod init github.com/seuusuario/mcpgateway`
2. Estrutura de pastas conforme `internal/` na seção de arquitetura
3. `docker-compose up` sobe postgres + redis + vault
4. `internal/config/types.go` — todas as structs de configuração
5. `internal/config/loader.go` — parser YAML básico (sem SecretOrValue ainda)
6. `internal/mcp/protocol.go` — tipos JSON-RPC + helpers
7. `internal/mcp/server_stdio.go` — `initialize`, `tools/list`, `tools/call`
8. `internal/gateway/handler.go` — execução HTTP simples (sem retry, sem auth)
9. `internal/store/migrations/001_initial.sql` + runner com golang-migrate
10. Auth type `none` e `bearer` via variável de ambiente direto

### Como testar

**Testes unitários (sem infra):**
```bash
go test ./internal/config/... -v
```
- Cria um YAML válido na memória e verifica que o loader monta a struct correta
- Cria um YAML com campo faltando e verifica que retorna erro claro

**Teste manual com Claude Desktop:**

Adicione ao `claude_desktop_config.json`:
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

Pergunte ao Claude Desktop: *"Quem é o usuário 5?"*
O gateway deve retornar os dados da JSONPlaceholder API.

**Entregável:** Claude Desktop → gateway → API pública funcionando.

---

## Semana 2 — Secret Resolver completo

**Objetivo:** Credenciais saem de vault/secrets manager, não de texto claro no YAML.

### O que construir

1. `internal/secrets/types.go` — `SecretRef`, `SecretValue`, interface `Provider`
2. `internal/secrets/provider_env.go` — lê `os.Getenv`
3. `internal/secrets/provider_aws.go` — AWS Secrets Manager via IAM Role
4. `internal/secrets/provider_vault.go` — HashiCorp Vault (AppRole + token direto)
5. `internal/secrets/resolver.go` — cache TTL + prefetch assíncrono
6. `internal/config/types.go` — adiciona `SecretOrValue` com `UnmarshalYAML`
7. `internal/config/validator.go` — rejeita literal em campo sensível

### Como testar

**Provider env (sem infra, roda em qualquer máquina):**
```go
// internal/secrets/provider_env_test.go
func TestEnvProvider(t *testing.T) {
    t.Setenv("MY_SECRET", "valor-teste")
    p := &EnvProvider{}
    sv, err := p.Get(context.Background(), SecretRef{Source: "env", Var: "MY_SECRET"})
    require.NoError(t, err)
    assert.Equal(t, "valor-teste", sv.Value)
}
```

**Provider AWS (mock do SDK com httptest):**
```go
func TestAWSProvider(t *testing.T) {
    // Sobe um servidor HTTP fake que imita o AWS Secrets Manager
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(map[string]string{
            "SecretString": `{"client_id":"test-id","client_secret":"test-secret"}`,
        })
    }))
    defer srv.Close()
    // Configura o client AWS apontando para srv.URL
    // ...
}
```

**Validator rejeita literal:**
```go
func TestValidatorRejectsLiteral(t *testing.T) {
    yaml := `
apis:
  crm:
    auth:
      type: oauth2_client_credentials
      client_secret: "senha-em-texto-claro"  # deve falhar
`
    _, err := config.LoadFromString(yaml)
    assert.ErrorContains(t, err, "client_secret cannot be a literal value")
}
```

**Teste com Vault local (requer `docker-compose up vault`):**
```bash
export VAULT_ADDR=http://localhost:8200
export VAULT_TOKEN=dev-root-token
vault kv put secret/dev/crm-credentials client_id=test-id client_secret=test-secret

go test ./internal/secrets/... -v -tags=integration
```

**Entregável:** Config com `source: aws_secrets_manager` funciona localmente com `AWS_PROFILE`.

---

## Semana 3 — Auth Manager com STS

**Objetivo:** Gateway obtém tokens do STS e cacheia por `(api, scope, caller)`.

### O que construir

1. `internal/auth/token_cache.go` — cache L1 (in-process ttlcache) + L2 (Redis)
2. `internal/auth/manager.go` — `GetToken`, `ApplyAuth`, `fetchClientCredentials`, `fetchTokenExchange`
3. `internal/auth/aws_sigv4.go` — assinatura de requests AWS
4. Aviso no validator para tools com `oauth2_token_exchange` no modo stdio

### Como testar

**Cache hit/miss (mock do STS com httptest):**
```go
func TestTokenCacheHit(t *testing.T) {
    callCount := 0
    sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        callCount++
        json.NewEncoder(w).Encode(map[string]interface{}{
            "access_token": "token-abc",
            "expires_in":   3600,
        })
    }))
    defer sts.Close()

    manager := newTestManager(sts.URL)

    token1, _ := manager.GetToken(ctx, "crm", "crm:read", "agente-bearer")
    token2, _ := manager.GetToken(ctx, "crm", "crm:read", "agente-bearer")

    assert.Equal(t, token1, token2)
    assert.Equal(t, 1, callCount) // STS chamado só uma vez
}
```

**Dois agentes recebem tokens diferentes:**
```go
func TestDifferentAgentsGetDifferentTokens(t *testing.T) {
    // agente-a e agente-b têm bearer tokens distintos
    // token_exchange deve gerar tokens diferentes para cada um
    tokenA, _ := manager.GetToken(ctx, "erp", "erp:read", "bearer-agente-a")
    tokenB, _ := manager.GetToken(ctx, "erp", "erp:read", "bearer-agente-b")
    assert.NotEqual(t, tokenA, tokenB)
}
```

**Teste com Redis (requer `docker-compose up redis`):**
```bash
go test ./internal/auth/... -v -tags=integration
```

**Entregável:** Token cacheado no Redis. Cache hit verificável nos logs (`cache_hit=true`).

---

## Semana 4 — Pipeline HTTP completo

**Objetivo:** Modo HTTP funcional de ponta a ponta com todos os middlewares.

### O que construir

1. `internal/middleware/agent_auth.go` — valida Bearer token do agente
2. `internal/middleware/scope_check.go` — verifica `allowed_tools`
3. `internal/middleware/rate_limiter.go` — sliding window no Redis por `agent×tool`
4. `internal/gateway/request_builder.go` — path params, query string, body
5. `internal/gateway/confirmation.go` — `require_confirmation` com TTL no Redis
6. `internal/store/async_logger.go` — batch insert via `pgx.CopyFrom`
7. `internal/retry/executor.go` — backoff exponencial com clonagem de body
8. `internal/transform/transformer.go` — `include_fields`, `exclude_fields`

### Como testar

**Testes de integração dos 6 fluxos principais (requer toda a infra):**

```bash
# Sobe infra
docker-compose up -d postgres redis vault

# Roda migrations
go run ./cmd/mcpgateway migrate --db "postgres://mcpgateway:dev_password@localhost:5432/mcpgateway"

# Roda testes de integração
go test ./... -v -tags=integration -race
```

**Fluxo 1 — Sucesso:**
```bash
curl -X POST http://localhost:8080/mcp \
  -H "Authorization: Bearer agent-key-valida" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_user","arguments":{"user_id":5}}}'
# Espera: 200 OK com dados do usuário
```

**Fluxo 2 — Scope negado (agente sem permissão):**
```bash
curl -X POST http://localhost:8080/mcp \
  -H "Authorization: Bearer agent-key-restrita" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_purchase_order","arguments":{...}}}'
# Espera: erro -32603 "tool not allowed for this agent"
```

**Fluxo 3 — Rate limit:**
```bash
# Dispara 70 requests em sequência (limite: 60/min)
for i in $(seq 1 70); do curl -s ... ; done
# Requests 61-70 devem retornar erro de rate limit
```

**Fluxo 4 — Validação de argumentos:**
```bash
# Passa user_id como string em vez de integer
-d '{"params":{"name":"get_user","arguments":{"user_id":"nao-e-numero"}}}'
# Espera: erro -32602 com mensagem de validação
```

**Fluxo 5 — STS indisponível:**
```bash
# Para o Vault temporariamente
docker-compose stop vault
# Faz chamada que precisa de credencial do Vault
# Espera: erro -32603 "acquiring auth token: ..."
docker-compose start vault
```

**Fluxo 6 — Retry:**
```go
// Sobe API fake que falha nas 2 primeiras chamadas e só na terceira retorna 200
// Configura max_attempts: 3 no YAML
// Verifica que a resposta final é sucesso e que o log mostra attempt_count=3
```

**Entregável:** `curl` no endpoint HTTP retorna resultado correto. Todos os 6 fluxos cobertos.

---

## Semana 5 — Providers restantes e graceful shutdown

**Objetivo:** Suporte completo a todos os ambientes corporativos.

### O que construir

1. `internal/secrets/provider_azure.go` — Azure Key Vault com Managed Identity
2. `internal/secrets/provider_gcp.go` — GCP Secret Manager com ADC
3. Auth type `mtls` — `http.Client` com certificado de cliente configurado
4. Graceful shutdown — drenagem do `AsyncCallLogger` antes de sair
5. `GET /health` com status real de cada componente

### Como testar

**Azure e GCP:** testados com mocks de HTTP ou contra ambientes de dev se disponíveis.

**Graceful shutdown:**
```bash
# Dispara 20 chamadas simultâneas
# Durante a execução, manda SIGTERM para o processo
# Verifica que todos os logs foram persistidos no banco (nenhum perdido)
go test ./internal/gateway/... -run TestGracefulShutdown -tags=integration
```

**Health check:**
```bash
curl http://localhost:8081/health
# Espera:
# {
#   "status": "healthy",
#   "postgres": "ok",
#   "redis": "ok",
#   "vault": "ok"
# }

# Para o Redis e checa novamente
docker-compose stop redis
curl http://localhost:8081/health
# "redis": "unreachable"
```

---

## Semana 6 — Dashboard web

**Objetivo:** Painel operacional com métricas e logs em `localhost:8081`.

### O que construir

1. `internal/admin/server.go` — HTTP server na porta 8081
2. Endpoints: `GET /api/tools`, `GET /api/metrics`, `GET /api/calls`, `GET /api/secrets/health`
3. Goroutine que refresha view materializada a cada 5 minutos
4. Frontend React: dashboard, lista de tools, call log, gráfico de chamadas por hora

### Como testar

**Admin API:**
```bash
# Faz 10 chamadas via MCP, depois verifica se aparecem no log
curl http://localhost:8081/api/calls?limit=10
curl http://localhost:8081/api/metrics
```

**Frontend:** abrir `localhost:8081` no browser e verificar visualmente que os dados aparecem.

---

## Semana 7 — Polimento e publicação

**Objetivo:** Produto pronto para portfólio e uso real.

### O que construir

1. `mcpgateway validate --config` com output legível para CI
2. `mcpgateway init` gera config de exemplo
3. GoReleaser para binários Linux/macOS/Windows
4. README com quickstart e GIF de demo
5. Examples: `local-dev/`, `aws/`, `vault/`, `kubernetes/`

### Como testar

**Validate no CI:**
```bash
# Deve passar
go run ./cmd/mcpgateway validate --config ./examples/local-dev/mcpgateway.yaml

# Deve falhar com mensagem clara
go run ./cmd/mcpgateway validate --config ./examples/invalid.yaml
echo "Exit code: $?"  # deve ser != 0
```

**Build multiplataforma:**
```bash
goreleaser release --snapshot --clean
ls dist/
# mcpgateway_linux_amd64
# mcpgateway_darwin_arm64
# mcpgateway_windows_amd64.exe
```

---

## Resumo: quando você pode testar o quê

| Momento | O que testar | Infra necessária |
|---|---|---|
| Dia 1 | Unit tests de config e secrets/env | Nenhuma |
| Fim semana 1 | Claude Desktop chamando JSONPlaceholder | Nenhuma |
| Semana 2 | Unit tests de providers com httptest mock | Nenhuma (Vault opcional) |
| Semana 3 | Cache hit/miss do auth com STS mockado | Redis |
| Semana 4 | Integration tests do pipeline HTTP completo | Postgres + Redis + Vault |
| Semana 5 | Graceful shutdown e health check | Postgres + Redis + Vault |
| Semana 6 | Dashboard com dados reais | Infra completa |

**Regra geral:** tudo que é lógica pura (validação, transformação, parsing) pode ser testado com `go test` desde o primeiro dia sem nenhuma infra rodando. A infra só é necessária a partir do momento que o código faz I/O real (Redis, Postgres, Vault, APIs externas).
