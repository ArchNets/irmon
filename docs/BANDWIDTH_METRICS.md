# How to Access Bandwidth Metrics

This guide explains all the ways to access your bandwidth monitoring data.

---

## 🎯 Quick Overview

Your bandwidth data is available in **3 places**:

1. **Prometheus Metrics** - Real-time, for Grafana dashboards
2. **SQLite Database** - Historical data, for custom queries
3. **JSON API** - Raw data from each agent

---

## 1️⃣ Prometheus Metrics

**Access:** `http://your-central-server:9090/metrics`

### Available Metrics

```prometheus
# Total bytes received by each server
irmon_server_bandwidth_rx_bytes{server="mahan-2"} 8595734528

# Total bytes transmitted by each server
irmon_server_bandwidth_tx_bytes{server="mahan-2"} 752238592

# Current receive speed (bytes per second)
irmon_server_bandwidth_rx_bytes_per_sec{server="mahan-2"} 102981632

# Current transmit speed (bytes per second)
irmon_server_bandwidth_tx_bytes_per_sec{server="mahan-2"} 6523904
```

### Example: Check Metrics via Command Line

```bash
curl http://localhost:9090/metrics | grep bandwidth
```

### Example: Grafana Queries

**Download Speed (Mbps):**

```promql
rate(irmon_server_bandwidth_rx_bytes[1m]) * 8 / 1000000
```

**Upload Speed (Mbps):**

```promql
rate(irmon_server_bandwidth_tx_bytes[1m]) * 8 / 1000000
```

**Total Traffic (Last Hour):**

```promql
increase(irmon_server_bandwidth_rx_bytes[1h]) + increase(irmon_server_bandwidth_tx_bytes[1h])
```

**Top Servers by Download:**

```promql
topk(5, irmon_server_bandwidth_rx_bytes)
```

---

## 2️⃣ SQLite Database

**Location:** `/var/lib/irmon/bandwidth.db`

### Quick Queries

**Latest reading for all servers:**

```bash
sqlite3 /var/lib/irmon/bandwidth.db \
  "SELECT server_name,
          datetime(timestamp) as time,
          rx_bytes,
          tx_bytes,
          rx_bytes_per_sec,
          tx_bytes_per_sec
   FROM bandwidth_history
   WHERE id IN (SELECT MAX(id) FROM bandwidth_history GROUP BY server_name)"
```

**History for specific server (last hour):**

```bash
sqlite3 /var/lib/irmon/bandwidth.db \
  "SELECT datetime(timestamp) as time,
          ROUND(rx_bytes_per_sec * 8 / 1000000.0, 2) as rx_mbps,
          ROUND(tx_bytes_per_sec * 8 / 1000000.0, 2) as tx_mbps
   FROM bandwidth_history
   WHERE server_name = 'mahan-2'
     AND timestamp > datetime('now', '-1 hour')
   ORDER BY timestamp DESC"
```

**Total bandwidth consumed:**

```bash
sqlite3 /var/lib/irmon/bandwidth.db \
  "SELECT server_name,
          ROUND(MAX(rx_bytes) / 1024.0 / 1024.0 / 1024.0, 2) as total_rx_gb,
          ROUND(MAX(tx_bytes) / 1024.0 / 1024.0 / 1024.0, 2) as total_tx_gb
   FROM bandwidth_history
   GROUP BY server_name"
```

**Database statistics:**

```bash
sqlite3 /var/lib/irmon/bandwidth.db \
  "SELECT COUNT(*) as total_records,
          COUNT(DISTINCT server_name) as servers,
          datetime(MIN(timestamp)) as oldest,
          datetime(MAX(timestamp)) as newest
   FROM bandwidth_history"
```

### Interactive Mode

```bash
# Open database shell
sqlite3 /var/lib/irmon/bandwidth.db

# List all tables
.tables

# Show schema
.schema bandwidth_history

# Pretty output
.mode column
.headers on

# Run any query
SELECT * FROM bandwidth_history ORDER BY timestamp DESC LIMIT 10;
```

---

## 3️⃣ Agent JSON API

**Access:** `http://tunnel-ip:9091/bandwidth` (via WireGuard tunnel only)

### Example Request

```bash
# From your central server (inside tunnel)
curl http://30.8.1.2:9091/bandwidth
```

### Response Format

```json
{
  "name": "wg0",
  "rx_bytes": 8595734528,
  "tx_bytes": 752238592,
  "rx_packets": 11234567,
  "tx_packets": 9876543,
  "timestamp": "2026-02-03T21:58:00+03:30",
  "rx_bytes_per_sec": 102981632,
  "tx_bytes_per_sec": 6523904
}
```

**Note:** This endpoint is only accessible within your WireGuard tunnel network (30.8.x.x addresses).

---

## 📊 Setting Up Grafana Dashboard

1. **Add Prometheus as Data Source:**
   - URL: `http://localhost:9090`
   - Access: Server (default)

2. **Create Dashboard Panels:**

   **Panel 1: Download Speed**
   - Query: `rate(irmon_server_bandwidth_rx_bytes[1m]) * 8 / 1000000`
   - Type: Graph
   - Unit: Mbps
   - Legend: `{{server}}`

   **Panel 2: Upload Speed**
   - Query: `rate(irmon_server_bandwidth_tx_bytes[1m]) * 8 / 1000000`
   - Type: Graph
   - Unit: Mbps

   **Panel 3: Current Stats (Table)**
   - Query: `irmon_server_bandwidth_rx_bytes_per_sec * 8 / 1000000`
   - Type: Table
   - Columns: Server, Download Speed, Upload Speed

   **Panel 4: Total Traffic**
   - Query: `increase(irmon_server_bandwidth_rx_bytes[1h]) / 1024 / 1024 / 1024`
   - Type: Stat
   - Unit: GB

---

## 🔍 Verification

**Check if data is being collected:**

```bash
# 1. Check Prometheus metrics
curl http://localhost:9090/metrics | grep irmon_server_bandwidth

# 2. Check SQLite has data
sqlite3 /var/lib/irmon/bandwidth.db "SELECT COUNT(*) FROM bandwidth_history"

# 3. Check agent endpoint (from central server)
curl http://30.8.1.2:9091/bandwidth

# 4. Check irmon logs
journalctl -u irmon -f | grep bandwidth
```

---

## 📝 Summary

| Method         | Access                         | Use Case                                 |
| -------------- | ------------------------------ | ---------------------------------------- |
| **Prometheus** | http://localhost:9090/metrics  | Real-time monitoring, Grafana dashboards |
| **SQLite**     | /var/lib/irmon/bandwidth.db    | Historical queries, custom reports       |
| **Agent API**  | http://30.8.x.x:9091/bandwidth | Direct agent data (debugging)            |

**Data Flow:**

```
Agent reads /proc/net/dev
  → Exposes on :9091/bandwidth
  → Central irmon fetches every 10s
  → Stores in SQLite + Prometheus
  → You access via queries/dashboards
```
