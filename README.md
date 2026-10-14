# karum-server

The **Go + Gin API gateway** for [Karum](https://github.com/karum-labs/karum-infra#readme) — the tenant-mix leasing agent. This is the single public entrypoint the Flutter client talks to. It geocodes vacancies, orchestrates `service-taste` and `service-ai` over gRPC, and streams job progress over SSE.

Part of the five-repo Karum layout:

| Repo | Role |
| --- | --- |
| [`karum-infra`](https://github.com/karum-labs/karum-infra) | Shared protos, Docker Compose, Cloud Run manifests |
| [`karum-client`](https://github.com/karum-labs/karum-client) | Flutter UI (web / desktop) |
| **`karum-server`** (this repo) | Go + Gin API gateway |
| [`karum-service-taste`](https://github.com/karum-labs/karum-service-taste) | Qloo integration, caching, taste scoring |
| [`karum-service-ai`](https://github.com/karum-labs/karum-service-ai) | Vertex AI, search grounding, pitch drafting |

## Run path

Requires **Go 1.26+** (see the version pin in [`karum-infra`](https://github.com/karum-labs/karum-infra)).

```bash
# Run the gateway with default (fixture) settings.
make run

# Or build and run the binary directly.
make build && ./bin/gateway
```

The gateway listens on **`:8080`** by default. Confirm it is healthy:

```bash
curl http://localhost:8080/health
```

```json
{
  "service": "gateway",
  "status": "ok",
  "mode": "fixture",
  "version": "dev",
  "uptime_seconds": 0,
  "time": "2026-10-12T00:00:00Z"
}
```

### Configuration

Every setting is read from the environment. All of them have working defaults, so the gateway boots with no `.env` file. The full list — including which process reads each variable — lives in [`karum-infra/.env.example`](https://github.com/karum-labs/karum-infra/blob/main/.env.example).

| Variable | Default | Purpose |
| --- | --- | --- |
| `GATEWAY_PORT` | `8080` | HTTP listen port |
| `GATEWAY_MODE` | `fixture` | `live`, `fixture`, or `degraded`; reported on `/health` |
| `GATEWAY_VERSION` | `dev` | Build version stamped into responses |
| `TASTE_ADDR` | `localhost:8081` | gRPC address of `service-taste` |
| `AI_ADDR` | `localhost:8082` | gRPC address of `service-ai` |
| `REDIS_ADDR` | `localhost:6379` | Redis cache address |
| `POSTGRES_DSN` | _(empty)_ | Postgres connection string |

### Health

`GET /health` reports the gateway's operational mode:

- `fixture` — replaying recorded payloads, no outbound calls
- `live` — real upstream credentials present
- `degraded` — an upstream dependency is down; running the deterministic autopilot

### Development

```bash
make lint   # gofmt check + go vet
make test   # go test -race ./...
make fmt    # rewrite files in place
make tidy   # sync go.mod / go.sum
```

### Docker

```bash
docker build -t karum-server .
docker run -p 8080:8080 karum-server
```

## Repository layout

```
cmd/gateway/          main.go — process entrypoint + graceful shutdown
internal/config/      environment-driven configuration
internal/httpapi/     Gin router + HTTP handlers
internal/geocode/     vacancy address → point (stub)
internal/orchestrator/ job pipeline (stub)
```

## Ownership

Karum is a two-person build (record: [`DECISIONS.md`](https://github.com/karum-labs/karum-infra/blob/main/DECISIONS.md)). **Side A** owns taste, score, agent, and eval; **side B** owns UI, map, drafts, deploy, and Devpost. This repo is **side B** — gateway, orchestration, persistence, and SSE. Its job states and error codes must stay in step with the shared protos in [`karum-infra`](https://github.com/karum-labs/karum-infra), which side A owns.

## Licence

MIT. See [`LICENSE`](LICENSE).
