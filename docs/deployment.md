# Deployment

How to run `leash serve` as a long-lived gateway: under systemd, in Docker or
compose, or as a Kubernetes sidecar, with health checks and metrics wired up.
leash is one static binary with a SQLite or Postgres ledger; there is nothing
else to install.

leash does not terminate TLS in v0.1. Front it with an ingress or load balancer
that does, and let leash listen on plain HTTP behind it.

## systemd

Restarts are safe: the ledger is the source of truth, so a restarted process
folds the journal and resumes every run's budget where it left off. That makes
`Restart=on-failure` the right policy - a crash never resets a budget.

```ini
# /etc/systemd/system/leash.service
[Unit]
Description=leash spend governor
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=leash
Group=leash
# The DB path must be writable by this user.
Environment=LEASH_DB=/var/lib/leash/leash.db
Environment=LEASH_PRICES=/etc/leash/prices.json
ExecStart=/usr/local/bin/leash serve \
  --listen 127.0.0.1:8088 \
  --admin 127.0.0.1:9090 \
  --max-cost 20 \
  --log-format json
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
```

```sh
sudo useradd --system --home /var/lib/leash --shell /usr/sbin/nologin leash
sudo install -d -o leash -g leash /var/lib/leash
sudo systemctl enable --now leash
```

## Docker

The image is a multi-stage, CGO-free static build on `gcr.io/distroless/static`,
running as the nonroot uid `65532`. It exposes `:8088`, declares a `/data`
volume, and defaults to `serve --listen :8088 --db /data/leash.db`.

```sh
make docker IMAGE=leash:local        # or: docker build -t leash:local .

# The volume must be writable by uid 65532 (the nonroot user in the image).
docker volume create leash-data
docker run --rm -p 8088:8088 -p 9090:9090 \
  -v leash-data:/data \
  leash:local serve --listen :8088 --admin :9090 --db /data/leash.db --max-cost 20
```

Pass configuration as flags after the image name, or as `-e LEASH_*` environment
variables.

## docker-compose (no-key demo)

`docker-compose.yml` pairs the gateway with the std-lib fake upstream, so a
boundary trips end to end with no real key and no spend. The gateway is capped at
`--max-calls 5`, so the sixth call returns the 429 boundary body; the admin
listener is on `:9090`.

```sh
docker compose up --build
# in another shell:
for i in $(seq 1 6); do
  curl -s -XPOST localhost:8088/v1/chat/completions \
    -H 'X-Loop-Id: demo' -d '{"model":"demo-model"}'; echo
done
curl -s localhost:9090/metrics | grep leash_calls_total
```

## Kubernetes sidecar

Run leash as a sidecar in the agent's Pod. The two containers share `localhost`,
so the agent points its base URLs at the sidecar and never leaves the Pod.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: agent
spec:
  containers:
    - name: agent
      image: your/agent:latest
      env:
        - name: OPENAI_BASE_URL
          value: http://127.0.0.1:8088/v1
        - name: ANTHROPIC_BASE_URL
          value: http://127.0.0.1:8088
    - name: leash
      image: your-registry/leash:latest
      args:
        - serve
        - --listen=127.0.0.1:8088
        - --admin=127.0.0.1:9090
        - --db=/data/leash.db
        - --max-cost=20
      ports:
        - containerPort: 9090
          name: admin
      livenessProbe:
        httpGet: { path: /healthz, port: admin }
      readinessProbe:
        httpGet: { path: /readyz, port: admin }
      volumeMounts:
        - name: ledger
          mountPath: /data
  volumes:
    # emptyDir resets on Pod restart. Use a PVC if the budget must survive one.
    - name: ledger
      emptyDir: {}
```

A per-Pod sidecar is one governor per SQLite ledger, which is the supported
SQLite model. For a budget that must outlive the Pod, back `/data` with a
PersistentVolumeClaim, or use a Postgres ledger (see below and
[durability.md](durability.md)).

## Health checks and metrics

`--admin ADDR` starts a second HTTP server, separate from the proxy listener so
it never collides with proxied API paths and can sit on its own network segment.
It serves three endpoints:

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | liveness: always `200 ok` while the process runs |
| `GET /readyz` | readiness: `200 ready` when a ledger read succeeds within 1s, else `503 not ready` |
| `GET /metrics` | Prometheus text exposition (`text/plain; version=0.0.4`) |

A Prometheus scrape targeting the admin port:

```yaml
scrape_configs:
  - job_name: leash
    metrics_path: /metrics
    static_configs:
      - targets: ["leash-host:9090"]
```

The exposed series carry no run-id labels: run ids are unbounded cardinality, and
per-run data lives in the ledger (that is what `leash ps` reads). See
[operations.md](operations.md) for which series to alert on.

## Environment-variable configuration

Every shared flag also reads a `LEASH_`-prefixed variable, named mechanically
from the flag. An explicit flag beats the environment beats the default.

| Variable | Flag |
|---|---|
| `LEASH_DB` | `--db` |
| `LEASH_LISTEN` | `--listen` |
| `LEASH_ADMIN` | `--admin` |
| `LEASH_STANDBY` | `--standby` |
| `LEASH_REQUIRE_RUN_ID` | `--require-run-id` |
| `LEASH_AUTH_TOKEN` | `--auth-token` (preferred, keeps the secret out of the process list) |
| `LEASH_MAX_RUNS` | `--max-runs` |
| `LEASH_MAX_COST` | `--max-cost` |
| `LEASH_MAX_CALLS` | `--max-calls` |
| `LEASH_DEADLINE` | `--deadline` |
| `LEASH_RATE` | `--rate` |
| `LEASH_STALL` | `--stall` |
| `LEASH_PRICES` | `--prices` |
| `LEASH_COMPUTE_RATE` | `--compute-rate` |
| `LEASH_UPSTREAM` | `--upstream` |
| `LEASH_MAX_BODY_BYTES` | `--max-body-bytes` |
| `LEASH_UPSTREAM_HEADER_TIMEOUT` | `--upstream-header-timeout` |
| `LEASH_LOG_LEVEL` | `--log-level` |
| `LEASH_LOG_FORMAT` | `--log-format` |

## High availability

With a Postgres ledger the governance lease is a real cross-process advisory
lock, so exactly one instance governs a ledger at a time. Run a second instance
with `--standby`: it waits for the lease and takes over when the primary steps
down. See [operations.md](operations.md) for the failover runbook.
