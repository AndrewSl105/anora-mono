#!/bin/sh
# Fleetpocket: add this computer to {{.Server}}
# Usage: curl -fsSL {{.Server}}/api/v1/node/install | sh
# Installs fleetpocket-node into ~/.fleetpocket/bin (no sudo) and runs
# `fleetpocket-node setup`, which checks Docker, connects your agents and
# pairs the computer with your Fleetpocket.
set -eu

SERVER='{{.Server}}'
DOWNLOAD='{{.Download}}'

case "$(uname -s)/$(uname -m)" in
  Darwin/arm64) target=darwin-arm64 ;;
  Darwin/x86_64) target=darwin-amd64 ;;
  Linux/x86_64 | Linux/amd64) target=linux-amd64 ;;
  Linux/aarch64 | Linux/arm64) target=linux-arm64 ;;
  *)
    echo "Fleetpocket: $(uname -s)/$(uname -m) is not supported yet." >&2
    exit 1
    ;;
esac

dir="$HOME/.fleetpocket/bin"
mkdir -p "$dir"
echo "Downloading fleetpocket-node ($target)…"
curl -fsSL "$DOWNLOAD/fleetpocket-node-$target" -o "$dir/fleetpocket-node.download"
chmod +x "$dir/fleetpocket-node.download"
mv -f "$dir/fleetpocket-node.download" "$dir/fleetpocket-node"

# The setup asks questions: read them from the terminal, not from this pipe.
if [ -r /dev/tty ]; then
  exec "$dir/fleetpocket-node" setup --server "$SERVER" </dev/tty
fi
exec "$dir/fleetpocket-node" setup --server "$SERVER"
