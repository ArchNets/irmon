#!/bin/bash
set -e

# irmon-agent installer
# Usage: curl -sSL https://raw.githubusercontent.com/ArchNets/irmon/master/install-agent.sh | bash

REPO="ArchNets/irmon"
BINARY="irmon-agent"
INSTALL_DIR="/usr/local/bin"
SERVICE_FILE="/etc/systemd/system/irmon-agent.service"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Check root
if [ "$EUID" -ne 0 ]; then
    error "Please run as root: sudo bash install-agent.sh"
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
info "Installed: $(${BINARY} --help 2>&1 | head -1)"

# Prompt for listen addresses
echo ""
echo -e "${YELLOW}Configuration${NC}"
echo "Enter the IP addresses to listen on (comma-separated)."
echo "Example: 30.7.0.1:8080,30.8.0.1:8080"
echo ""
read -p "Listen addresses [0.0.0.0:8080]: " LISTEN_ADDRS
LISTEN_ADDRS=${LISTEN_ADDRS:-"0.0.0.0:8080"}

# Create systemd service
info "Creating systemd service..."
cat > "$SERVICE_FILE" << EOF
[Unit]
Description=irmon Agent - Health Check Endpoint
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${INSTALL_DIR}/${BINARY} -listen ${LISTEN_ADDRS}
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=irmon-agent

[Install]
WantedBy=multi-user.target
EOF

# Enable and start service
info "Enabling and starting irmon-agent service..."
systemctl daemon-reload
systemctl enable irmon-agent
systemctl start irmon-agent

# Show status
echo ""
info "Installation complete!"
echo ""
echo -e "${GREEN}Service status:${NC}"
systemctl status irmon-agent --no-pager -l || true
echo ""
echo -e "${GREEN}Listening on:${NC} ${LISTEN_ADDRS}"
echo ""
echo -e "${YELLOW}Commands:${NC}"
echo "  View logs:     journalctl -u irmon-agent -f"
echo "  Restart:       systemctl restart irmon-agent"
echo "  Stop:          systemctl stop irmon-agent"
echo "  Uninstall:     systemctl disable irmon-agent && rm ${INSTALL_DIR}/${BINARY} ${SERVICE_FILE}"
echo ""
echo -e "${GREEN}Test the health endpoint:${NC}"
for addr in $(echo $LISTEN_ADDRS | tr ',' ' '); do
    echo "  curl http://${addr}/health"
done
