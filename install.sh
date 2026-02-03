#!/bin/bash
set -e

# irmon installer (monitor)
# Usage: curl -sSL https://raw.githubusercontent.com/ArchNets/irmon/master/install.sh | bash

REPO="ArchNets/irmon"
BINARY="irmon"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/irmon"
SERVICE_FILE="/etc/systemd/system/irmon.service"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Check root
if [ "$EUID" -ne 0 ]; then
    error "Please run as root: sudo bash install.sh"
fi

# Detect architecture
ARCH=$(uname -m)
case $ARCH in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    *) error "Unsupported architecture: $ARCH" ;;
esac

info "Detected architecture: $ARCH"

# Get latest version
info "Fetching latest version..."
VERSION=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
if [ -z "$VERSION" ]; then
    error "Failed to get latest version"
fi
info "Latest version: $VERSION"

# Download binary
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY}-linux-${ARCH}"
info "Downloading ${BINARY} from ${DOWNLOAD_URL}..."
curl -sSL "$DOWNLOAD_URL" -o "/tmp/${BINARY}"
chmod +x "/tmp/${BINARY}"

# Install binary
info "Installing to ${INSTALL_DIR}/${BINARY}..."
mv "/tmp/${BINARY}" "${INSTALL_DIR}/${BINARY}"

# Verify installation
if ! command -v ${BINARY} &> /dev/null; then
    error "Installation failed"
fi
info "Installed: irmon"

# Create config directory
mkdir -p "$CONFIG_DIR"

# Create example config if not exists
if [ ! -f "${CONFIG_DIR}/config.yaml" ]; then
    info "Creating example config at ${CONFIG_DIR}/config.yaml..."
    cat > "${CONFIG_DIR}/config.yaml" << 'EOF'
# irmon configuration
# Edit this file to add your servers and Cloudflare credentials

check_config:
  interval: 30s
  timeout: 10s
  failure_threshold: 3
  recovery_threshold: 2
  flapping_window: 5m

scoring:
  weights:
    tcp: 100
    icmp: 20
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
  # Add your Iranian servers here
  # Example:
  # - name: "iran-1"
  #   origin_ip: "185.x.x.x"    # Public IP in Cloudflare
  #   tunnel_ip: "30.1.0.1"
  #   protocols:
  #     - type: tcp
  #       endpoint: "30.1.0.1:8080"
  #       timeout: 5000
  #     - type: icmp
  #       endpoint: "30.1.0.1"
  #       timeout: 3000

logging:
  format: json
  level: info

metrics:
  enabled: true
  address: ":9090"
  path: /metrics
EOF
    warn "Please edit ${CONFIG_DIR}/config.yaml to add your servers!"
else
    info "Config already exists at ${CONFIG_DIR}/config.yaml"
fi

# Create environment file
if [ ! -f "${CONFIG_DIR}/env" ]; then
    info "Creating environment file at ${CONFIG_DIR}/env..."
    cat > "${CONFIG_DIR}/env" << 'EOF'
# Cloudflare credentials
CF_API_TOKEN=your-api-token-here
CF_ACCOUNT_ID=your-account-id-here
CF_POOL_ID=your-pool-id-here
EOF
    chmod 600 "${CONFIG_DIR}/env"
    warn "Please edit ${CONFIG_DIR}/env with your Cloudflare credentials!"
fi

# Create systemd service
info "Creating systemd service..."
cat > "$SERVICE_FILE" << EOF
[Unit]
Description=irmon - Iran-Aware Health Monitoring System
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${INSTALL_DIR}/${BINARY} run -config ${CONFIG_DIR}/config.yaml
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=irmon
EnvironmentFile=${CONFIG_DIR}/env

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd
systemctl daemon-reload

echo ""
info "Installation complete!"
echo ""
echo -e "${YELLOW}Next steps:${NC}"
echo "  1. Edit config: nano ${CONFIG_DIR}/config.yaml"
echo "  2. Add Cloudflare credentials: nano ${CONFIG_DIR}/env"
echo "  3. Start service: systemctl start irmon"
echo ""
echo -e "${YELLOW}Commands:${NC}"
echo "  Menu:          ${BINARY}  (interactive management)"
echo "  Start:         systemctl start irmon"
echo "  Enable:        systemctl enable irmon"
echo "  View logs:     journalctl -u irmon -f"
echo "  Status:        systemctl status irmon"
echo ""
echo -e "${GREEN}Metrics available at:${NC} http://localhost:9090/metrics"
