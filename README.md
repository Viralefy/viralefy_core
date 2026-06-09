# viralefy_core

Motor de domínio do Viralefy. Orquestra catálogo, checkout, usuários, pedidos, gateways, recargas, suporte, reviews, A/B, fraude, anti-abuso, multi-moeda e webhooks.

Sucessor direto do antigo `viralefy_api` (Go monolítico). Origem: fork 1:1 do api em 2026-06-09 — paridade total preservada, mesmo schema Postgres, mesmas migrations, mesma API HTTP. A única mudança nesta etapa é o **nome**: o monolito vira `core`, e o nome `viralefy_api` é liberado pra um dispatcher Rust focado em segurança/borda na Fase 9.

Plano arquitetural completo: [`viralefy_archive/PHASE-9-ARCHITECTURE.md`](https://github.com/Viralefy/viralefy_archive/blob/main/PHASE-9-ARCHITECTURE.md).

## O que mudou em relação a viralefy_api

- `module github.com/Viralefy/viralefy_core` (era `github.com/viralefy/viralefy_api`)
- `cmd/core/main.go` (era `cmd/api/main.go`)
- Binário canônico: `viralefy-core` (era `viralefy-api`)
- Service systemd alvo: `viralefy-core.service`
- Comentários internos atualizados

**Não mudou:** comportamento HTTP, schema DB, migrations (tracker `schema_migrations`), endpoints, configuração via env, cron jobs, integrações externas (Stripe, Heleket, AbacatePay, Resend, MinIO, Telegram).

## Diretrizes

Siga [diretrizes.md](https://github.com/Viralefy/viralefy_archive/blob/main/diretrizes.md) e [AGENTS.md](https://github.com/Viralefy/viralefy_archive/blob/main/AGENTS.md).

## Rodar local

```bash
# Postgres já rodando (compose ou local)
export DATABASE_URL=postgres://viralefy:viralefy@localhost:5432/viralefy?sslmode=disable
go run ./cmd/core
```

## CLI (sub-comandos)

```bash
viralefy-core migrate status     # lista migrations + estado
viralefy-core migrate up         # aplica pendentes (idempotente)
viralefy-core migrate backfill   # marca tudo aplicado sem rodar SQL (one-shot prod legacy)
viralefy-core migrate version    # última migration aplicada
viralefy-core seed               # popula seed inicial (idempotente, DO NOTHING)
viralefy-core                    # sobe HTTP server
```

## Endpoints principais

55 rotas HTTP organizadas em 4 buckets:

- **Public** (sem auth): `/v1/plans`, `/v1/categories`, `/v1/currencies`, `/v1/checkout`, `/v1/webhooks/{stripe,heleket,woovi,abacatepay,resend}`
- **User auth** (Bearer JWT user): `/v1/me/*`, `/v1/auth/user/*` (32 rotas)
- **Admin auth** (Bearer JWT admin + RBAC): `/v1/admin/*` (52 rotas)
- **API key** (X-API-Key, read-only B2B): `/v2/plans`, `/v2/orders/{id}/status`
- **Internal** (X-Internal-Token, loopback only): `/internal/v1/payment-confirmed`

## Tests

```bash
go test -count=1 ./...
```

Cobertura concentrada em `internal/application/` (business logic) e `internal/interface/http/` (auth/security middleware).

## Status

Fork inicial em 2026-06-09. **Ainda NÃO está em prod** — `viralefy_api` legacy continua servindo. Cutover planejado em fase seguinte da PHASE-9 com deploy paralelo + canary.
