# irmon - Iran-Aware Health Monitoring System

A Go-based, censorship-aware health monitoring and traffic control system designed specifically for Iranian network conditions.

## Two Components

| Binary        | Runs On            | Purpose                                      |
| ------------- | ------------------ | -------------------------------------------- |
| `irmon`       | Non-Iranian server | Monitors Iranian servers, updates Cloudflare |
| `irmon-agent` | Iranian servers    | Provides health check endpoint               |

```
┌────────────────────────────────────────────────────────────────┐
│              NON-IRANIAN SERVER (Germany, etc.)                 │
│                                                                 │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                        irmon                              │  │
│  │  • Checks health of all Iranian servers                   │  │
│  │  • Calculates scores                                      │  │
│  │  • Updates Cloudflare weights                             │  │
│  └──────────────────────────────────────────────────────────┘  │
│                              │                                  │
└──────────────────────────────┼──────────────────────────────────┘
                               │ Checks via tunnel IPs
        ┌──────────────────────┼──────────────────────┐
        │                      │                      │
        ▼                      ▼                      ▼
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│ Iran Server 1│     │ Iran Server 2│     │ Iran Server 3│
│              │     │              │     │              │
│ irmon-agent  │     │ irmon-agent  │     │ irmon-agent  │
│ :8080/health │     │ :8080/health │     │ :8080/health │
└──────────────┘     └──────────────┘     └──────────────┘
```

## Quick Start

### 1. Build Both Binaries

```bash
# Development build
go build -o irmon ./cmd/irmon
go build -o irmon-agent ./cmd/irmon-agent

# Production build (smaller binaries, ~40% size reduction)
go build -ldflags="-s -w" -o irmon ./cmd/irmon
go build -ldflags="-s -w" -o irmon-agent ./cmd/irmon-agent
```

| Binary        | Dev Build | Production Build |
| ------------- | --------- | ---------------- |
| `irmon`       | 14MB      | 9.8MB            |
| `irmon-agent` | 9.4MB     | 6.5MB            |

### 2. Deploy irmon-agent on Iranian Servers

```bash
# Copy binary to each Iranian server
scp irmon-agent iran-server-1:/usr/local/bin/

# On each Iranian server, run:
irmon-agent -listen 40.1.1.1:8080   # Use the tunnel IP
```

Or install as a service:

```bash
# Copy service file
sudo cp irmon-agent.service /etc/systemd/system/

# Edit and set the correct LISTEN_ADDR
sudo systemctl edit irmon-agent
# Add: Environment=LISTEN_ADDR=40.1.1.1:8080

# Enable and start
sudo systemctl enable --now irmon-agent
```

### 3. Configure irmon on Non-Iranian Server

Create `/etc/irmon/config.yaml`:

```yaml
check_config:
  interval: 30s
  timeout: 10s
  failure_threshold: 3
  recovery_threshold: 2
  flapping_window: 5m

scoring:
  weights:
    tcp: 100 # Primary check
    icmp: 20 # Secondary check
  thresholds:
    fully_usable: 80
    degraded: 20

cloudflare:
  api_token: "${CF_API_TOKEN}"
  account_id: "${CF_ACCOUNT_ID}"
  pool_id: "${CF_POOL_ID}"
  rate_limit: 5
  ttl: 30

servers:
  - name: "iran-1"
    origin_ip: "185.1.1.1" # Public IP in Cloudflare
    tunnel_ip: "40.1.1.1"
    protocols:
      - type: tcp
        endpoint: "40.1.1.1:8080" # irmon-agent endpoint
        timeout: 5000
      - type: icmp
        endpoint: "40.1.1.1"
        timeout: 3000

  - name: "iran-2"
    origin_ip: "185.1.1.2"
    tunnel_ip: "40.1.2.1"
    protocols:
      - type: tcp
        endpoint: "40.1.2.1:8080"
        timeout: 5000
      - type: icmp
        endpoint: "40.1.2.1"
        timeout: 3000

  # ... add all 7 servers

logging:
  format: json
  level: info

metrics:
  enabled: true
  address: ":9090"
  path: /metrics
```

### 4. Run irmon

```bash
export CF_API_TOKEN="your-token"
export CF_ACCOUNT_ID="your-account-id"
export CF_POOL_ID="your-pool-id"

./irmon -config /etc/irmon/config.yaml
```

---

## irmon-agent Endpoints

| Endpoint  | Method    | Description                    |
| --------- | --------- | ------------------------------ |
| `/health` | GET       | JSON health status             |
| `/status` | GET       | Detailed status with local IPs |
| `/ws`     | WebSocket | For WS/WSS health checks       |

Example response from `/health`:

```json
{
  "status": "ok",
  "timestamp": "2024-02-03T12:00:00Z",
  "uptime": "2h30m",
  "hostname": "iran-server-1",
  "version": "1.0.0"
}
```

---

## irmon Scoring

| Check              | Weight | Meaning             |
| ------------------ | ------ | ------------------- |
| TCP to irmon-agent | 100    | Server is reachable |
| ICMP ping          | 20     | Basic connectivity  |

| State        | Score | Cloudflare            |
| ------------ | ----- | --------------------- |
| FULLY_USABLE | ≥80   | weight = score        |
| DEGRADED     | 20-79 | weight = score        |
| IRAN_ONLY    | <20   | weight = 0 (disabled) |

---

## Prometheus Metrics (irmon)

Available at `:9090/metrics`:

| Metric                  | Description                                     |
| ----------------------- | ----------------------------------------------- |
| `irmon_server_score`    | Health score per server                         |
| `irmon_server_state`    | State (0=IRAN_ONLY, 1=DEGRADED, 2=FULLY_USABLE) |
| `irmon_server_weight`   | Cloudflare weight                               |
| `irmon_protocol_check`  | Check result (0=fail, 1=success)                |
| `irmon_protocol_rtt_ms` | RTT in milliseconds                             |

---

## Systemd Services

### irmon.service (Non-Iranian server)

```bash
sudo cp irmon.service /etc/systemd/system/
sudo systemctl enable --now irmon
```

### irmon-agent.service (Iranian servers)

```bash
sudo cp irmon-agent.service /etc/systemd/system/
# Edit to set correct LISTEN_ADDR
sudo systemctl enable --now irmon-agent
```

---

## Environment Variables

| Variable        | Used By     | Description           |
| --------------- | ----------- | --------------------- |
| `CF_API_TOKEN`  | irmon       | Cloudflare API token  |
| `CF_ACCOUNT_ID` | irmon       | Cloudflare account ID |
| `CF_POOL_ID`    | irmon       | Cloudflare pool ID    |
| `LISTEN_ADDR`   | irmon-agent | Address to listen on  |

---

## License

MIT License
