#!/bin/bash
set -euo pipefail

# Resolved the same way as install-kata.sh: the RuntimeClass manifest is applied
# from the sibling k8s/ directory when this script travels with it.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# install-gvisor.sh — Install gVisor (runsc) on a Linux Kubernetes node.
#
# Run this script directly on each node you want to use for gVisor agents.
# After running on each node, apply the RuntimeClass and register the node
# from any machine that has kubectl access to the cluster (see docs).
#
# Usage:
#   sudo bash install-gvisor.sh
#
# Or pipe directly from the repo:
#   curl -fsSL https://raw.githubusercontent.com/wso2/agent-manager/main/deployments/setup/install-gvisor.sh \
#     | sudo bash
#
# Optional: host-network passthrough for runsc (keeps syscall isolation but uses
# the host network stack inside the pod netns). Use only if the default userspace
# netstack cannot carry cross-node Service traffic on your CNI/overlay:
#   GVISOR_NETWORK_HOST=true sudo -E bash install-gvisor.sh
#
# Prerequisites:
#   - Ubuntu 20.04+, Debian 11+, RHEL 8+, Amazon Linux 2023, or any Linux with
#     containerd managed by systemd
#   - x86_64 or aarch64 (arm64) architecture
#   - Running containerd (systemctl is-active containerd)
#   - Internet access to storage.googleapis.com
#   - zstd or bzip2
#
# Idempotent: safe to re-run. Already-installed components are skipped.

echo "=== Installing gVisor (runsc) isolation tier ==="

GVISOR_NETWORK_HOST="${GVISOR_NETWORK_HOST:-false}"

# Must run as root
if [ "$(id -u)" != "0" ]; then
    echo "❌ This script must be run as root. Use: sudo bash install-gvisor.sh"
    exit 1
fi

# --- Detect architecture ---
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)        GVISOR_ARCH="x86_64" ;;
    aarch64|arm64) GVISOR_ARCH="aarch64" ;;
    *)
        echo "❌ Unsupported architecture: ${ARCH}"
        echo "   gVisor supports x86_64 and aarch64 only."
        exit 1
        ;;
esac
echo "   Architecture: ${ARCH}"

# --- Check containerd is present and running ---
if ! command -v containerd &>/dev/null; then
    echo "❌ containerd not found. Install containerd before running this script."
    echo "   See: https://docs.docker.com/engine/install/"
    exit 1
fi

if ! systemctl is-active containerd &>/dev/null; then
    echo "❌ containerd service is not running."
    echo "   Start it with: systemctl start containerd"
    exit 1
fi

CONTAINERD_VERSION="$(containerd --version | grep -oE '[0-9]+\.[0-9]+' | head -1)"
echo "   containerd: v${CONTAINERD_VERSION}"

# --- Install runsc ---
if command -v runsc &>/dev/null && runsc --version &>/dev/null 2>&1; then
    echo "✅ runsc already installed ($(runsc --version 2>&1 | head -1)) — skipping download"
else
    # extract the runsc and containerd-shim-runsc-v1 from the tarball
    if command -v zstd &>/dev/null; then
        TARBALL="gvisor.tar.zstd"; DECOMPRESS=(zstd -dc)
    elif command -v bzip2 &>/dev/null; then
        TARBALL="gvisor.tar.bz2"; DECOMPRESS=(bzip2 -dc)
    else
        echo "❌ zstd or bzip2 is required to unpack the gVisor release. Install one and re-run."
        exit 1
    fi

    echo "📥 Downloading gVisor release (${GVISOR_ARCH})..."
    BASE="https://storage.googleapis.com/gvisor/releases/release/latest/${GVISOR_ARCH}"
    # Download into a private temp dir (not predictable /tmp paths) and remove it on exit.
    #the tarball is ~130 MB and /tmp may be RAM-backed.
    GVISOR_TMP="$(mktemp -d /var/tmp/gvisor.XXXXXX)"
    trap 'rm -rf "${GVISOR_TMP}"' EXIT
    curl -fsSL --retry 3 "${BASE}/${TARBALL}" -o "${GVISOR_TMP}/${TARBALL}"
    # gVisor publishes a .sha512 alongside the tarball; verify before trusting it.
    curl -fsSL --retry 3 "${BASE}/${TARBALL}.sha512" -o "${GVISOR_TMP}/${TARBALL}.sha512"
    ( cd "${GVISOR_TMP}" && sha512sum -c "${TARBALL}.sha512" ) || {
        echo "❌ gVisor release checksum verification failed — aborting."
        exit 1
    }
    "${DECOMPRESS[@]}" "${GVISOR_TMP}/${TARBALL}" \
        | tar -xf - -C "${GVISOR_TMP}" runsc containerd-shim-runsc-v1
    chmod +x "${GVISOR_TMP}/runsc" "${GVISOR_TMP}/containerd-shim-runsc-v1"
    mv "${GVISOR_TMP}/runsc" /usr/local/bin/runsc
    mv "${GVISOR_TMP}/containerd-shim-runsc-v1" /usr/local/bin/containerd-shim-runsc-v1
    echo "   ✅ runsc $(runsc --version 2>&1 | head -1) installed"
fi

# --- Configure containerd ---
CONTAINERD_CONFIG="/etc/containerd/config.toml"

if [ -f "$CONTAINERD_CONFIG" ] && grep -q "runsc" "$CONTAINERD_CONFIG" 2>/dev/null; then
    echo "✅ containerd already configured for runsc — skipping"
else
    echo "⚙️  Adding runsc runtime to containerd config (${CONTAINERD_CONFIG})..."

    if [ ! -f "$CONTAINERD_CONFIG" ]; then
        # Generate a default config if none exists (common on fresh nodes)
        mkdir -p "$(dirname "$CONTAINERD_CONFIG")"
        containerd config default > "$CONTAINERD_CONFIG"
        echo "   ✅ Generated default containerd config"
    fi

    # Append the runsc runtime block.
    # Both containerd v1.x and v2.x honour the grpc.v1.cri plugin config path.
    if [ "$GVISOR_NETWORK_HOST" = "true" ]; then
        cat >> "$CONTAINERD_CONFIG" <<'EOF'

# gVisor (runsc) runtime — added by install-gvisor.sh (host-network passthrough)
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc]
  runtime_type = "io.containerd.runsc.v1"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc.options]
    TypeUrl = "io.containerd.runsc.v1.options"
    ConfigPath = "/etc/containerd/runsc.toml"
EOF
        printf '[runsc_config]\n  network = "host"\n' > /etc/containerd/runsc.toml
        echo "   ✅ containerd config updated (runsc --network=host)"
    else
        cat >> "$CONTAINERD_CONFIG" <<'EOF'

# gVisor (runsc) runtime — added by install-gvisor.sh
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc]
  runtime_type = "io.containerd.runsc.v1"
EOF
        echo "   ✅ containerd config updated"
    fi
fi

# --- Restart containerd ---
# Only containerd restarts — kubelet and running pods are unaffected.
echo "🔄 Restarting containerd to load the new runtime..."
systemctl restart containerd

# Give containerd a moment to finish loading plugins
sleep 5

# Verify the shim is available (crictl is present on kubeadm/EKS/GKE/AKS nodes)
if command -v crictl &>/dev/null; then
    if crictl info 2>/dev/null | grep -q "runsc"; then
        echo "   ✅ runsc runtime registered in containerd"
    else
        echo "   ⚠️  crictl info did not show runsc yet — containerd may still be loading plugins."
        echo "       Wait a few seconds and re-check: crictl info | grep runsc"
    fi
fi

echo ""
echo "✅ gVisor installed on this node."
echo ""
echo "Next steps — from a machine with kubectl access to your cluster:"
echo ""
echo "  1. Apply the RuntimeClass (once per cluster):"
if [ -f "$SCRIPT_DIR/../k8s/gvisor-runtimeclass.yaml" ]; then
    # Point at the copy already on disk: telling the reader to curl a different
    # one invites a version mismatch with the install they just ran.
    echo "     kubectl apply -f \"$SCRIPT_DIR/../k8s/gvisor-runtimeclass.yaml\""
else
    echo "     kubectl apply -f ${AMP_MANIFEST_BASE_URL:-https://raw.githubusercontent.com/wso2/agent-manager/${AMP_RELEASE_REF:-main}/deployments/k8s}/gvisor-runtimeclass.yaml"
fi
echo ""
echo "  2. Label and taint this node (replace <node-name> with: kubectl get nodes):"
echo "     kubectl label node <node-name> gvisor=true --overwrite"
echo "     kubectl taint node <node-name> gvisor=true:NoSchedule --overwrite"
echo ""
echo "  3. Ensure the Fluent Bit log DaemonSet tolerates the taint (so agent logs are collected):"
echo "     kubectl patch daemonset fluent-bit -n openchoreo-observability-plane --type=json \\"
echo "       -p='[{\"op\":\"add\",\"path\":\"/spec/template/spec/tolerations\",\"value\":[{\"operator\":\"Exists\"}]}]'"
echo ""
echo "  4. Verify:"
echo "     kubectl get runtimeclass gvisor"
echo "     kubectl get node <node-name> --show-labels"
