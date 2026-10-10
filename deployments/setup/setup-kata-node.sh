#!/bin/bash

# Adds a Kata Containers node to the EXISTING k3d cluster by joining a REAL (non-Docker) k3s
# agent directly to it — NOT by creating a k3d node container. (`make setup-kata` / this script.)
#
# Why not a k3d node container (like setup-gvisor-node.sh does for gVisor): Kata needs a real
# glibc/systemd/journald Linux node with /dev/kvm. A k3d node is a minimal container, and even
# with /dev/kvm passed through from the host, Kata's host<->guest vsock handshake hits walls a
# real host doesn't have (missing glibc loader, /dev/log, vhost_vsock — confirmed via live
# testing, see deployments/kata-isolation-tier.md §9: k3d cannot boot a Kata VM, full stop).
# Rather than throwing away the whole cluster and reinstalling everything on a bare host, this
# script does the SAME thing `k3d node create` does under the hood for gVisor (run `k3s agent`,
# joined to the existing server) — just without the Docker wrapper, so the resulting node is a
# genuine host. The k3d server (and every runc/gVisor agent already running) is untouched.
#
# What it does:
#   1. Pulls the join token + reachable address off the running k3d server container.
#   2. Installs k3s as a bare systemd agent on THIS host, version-matched to the cluster.
#   3. Verifies /dev/kvm is present directly on the host (no container nesting in the way).
#   4. Wires this node into the k3d-only bits that k3d normally provisions automatically for
#      its own node containers, but never touches for an externally-joined node: the
#      `host.k3d.internal` DNS alias (used by the in-cluster workflow-plane registry) and the
#      registry mirror config (`/etc/rancher/k3s/registries.yaml`) — without these, image pulls
#      for anything built by the workflow plane fail with "no such host" / TLS errors.
#   5. Hands off to install-kata.sh (the production kata-deploy path) with this node as the
#      target — kata-deploy's Helm chart wires k3s's containerd itself (see install-kata.sh).
#
# This is a DEV/TEST tool for exercising the Kata tier locally. It is NOT what a real customer
# would do — a production cluster wouldn't have `host.k3d.internal` at all, and would just run
# install-kata.sh directly against its own real nodes.
#
# Prerequisites:
#   - Must run on the SAME host as the k3d cluster's Docker daemon (uses `docker exec`/`docker
#     inspect` against the k3d server container directly).
#   - Host has /dev/kvm (nested virtualization) — e.g. a GCP VM created with
#     --enable-nested-virtualization on an Intel N2 machine.
#   - curl, docker, kubectl, helm available.
#
# Idempotent: safe to re-run — skips k3s install if already joined, skips /etc/hosts /
# registries.yaml changes if already present.
#
# Usage:  make setup-kata    (or:  ./setup-kata-node.sh)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/env.sh"
source "$SCRIPT_DIR/utils.sh"

echo "=== Joining a bare k3s node to cluster '${CLUSTER_NAME}' (for Kata) ==="

# --- Preconditions ---
if ! kubectl cluster-info --context "$CLUSTER_CONTEXT" &>/dev/null; then
    echo "❌ k3d cluster '$CLUSTER_CONTEXT' is not running. Run: make setup"
    exit 1
fi
kubectl config use-context "$CLUSTER_CONTEXT" >/dev/null

SERVER_CONTAINER="k3d-${CLUSTER_NAME}-server-0"
if ! docker ps --filter "name=${SERVER_CONTAINER}" --format '{{.Names}}' | grep -q "${SERVER_CONTAINER}"; then
    echo "❌ Could not find the running k3d server container (${SERVER_CONTAINER})."
    echo "   This script must run on the same host as the k3d cluster's Docker daemon."
    exit 1
fi

THIS_NODE="$(hostname)"

# --- 1. Install k3s as a bare agent (skip if already joined) ---
if systemctl is-active --quiet k3s-agent 2>/dev/null && kubectl get node "$THIS_NODE" &>/dev/null; then
    echo "✅ ${THIS_NODE} is already a k3s agent joined to this cluster — skipping install"
else
    echo "🔑 Fetching join token + server address from ${SERVER_CONTAINER}..."
    TOKEN=$(docker exec "$SERVER_CONTAINER" cat /var/lib/rancher/k3s/server/node-token)
    SERVER_IP=$(docker inspect "$SERVER_CONTAINER" --format "{{ (index .NetworkSettings.Networks \"k3d-${CLUSTER_NAME}\").IPAddress }}")
    if [ -z "$SERVER_IP" ]; then
        echo "❌ Could not determine the k3d server's docker-network IP"
        exit 1
    fi
    echo "   Server: ${SERVER_IP}:6443"

    # Match this node's k3s version to the cluster's, so client/server skew doesn't bite.
    # `k3s --version` prints e.g. "k3s version v1.32.9+k3s1 (abcdef12)"; the docker image tag
    # uses a hyphen (Docker tags can't contain '+') but the upstream release/install tag needs
    # the real '+' form or the get.k3s.io installer 404s.
    SERVER_K3S_VERSION=$(docker exec "$SERVER_CONTAINER" k3s --version | head -1 | awk '{print $3}')
    if [ -z "$SERVER_K3S_VERSION" ]; then
        echo "❌ Could not determine the server's k3s version"
        exit 1
    fi
    echo "   Matching k3s version: ${SERVER_K3S_VERSION}"

    if ! command -v k3s &>/dev/null; then
        echo "🚀 Installing k3s agent ${SERVER_K3S_VERSION} on ${THIS_NODE}..."
        curl -sfL https://get.k3s.io | \
            INSTALL_K3S_VERSION="${SERVER_K3S_VERSION}" \
            K3S_URL="https://${SERVER_IP}:6443" \
            K3S_TOKEN="${TOKEN}" \
            sh -
    else
        echo "✅ k3s binary already present — skipping install"
    fi

    echo "⏳ Waiting for ${THIS_NODE} to register and become Ready..."
    kubectl wait --context "$CLUSTER_CONTEXT" --for=condition=Ready "node/${THIS_NODE}" --timeout=120s
fi

# --- 2. Verify /dev/kvm directly on the host (no container nesting to hide behind here) ---
if [ -e /dev/kvm ]; then
    echo "✅ /dev/kvm present on ${THIS_NODE} — nested virtualization available"
else
    echo "❌ /dev/kvm is NOT available on ${THIS_NODE}."
    echo "   - GCP: create the VM with --enable-nested-virtualization on an Intel N2 machine."
    echo "   - Verify:  ls -l /dev/kvm   (and cat /sys/module/kvm_intel/parameters/nested → Y)"
    exit 1
fi

# --- 3. Wire the k3d-only bits this node never got automatically ---
# host.k3d.internal: k3d bakes this into its own node containers' /etc/hosts; an externally
# joined node never gets it, so image pulls from the in-cluster workflow-plane registry
# (host.k3d.internal:10082) fail with "no such host".
HOST_K3D_IP=$(docker network inspect "k3d-${CLUSTER_NAME}" --format '{{ (index .IPAM.Config 0).Gateway }}' 2>/dev/null || true)
if [ -n "$HOST_K3D_IP" ]; then
    if ! grep -q "host.k3d.internal" /etc/hosts 2>/dev/null; then
        echo "🔧 Adding host.k3d.internal (${HOST_K3D_IP}) to /etc/hosts..."
        echo "${HOST_K3D_IP} host.k3d.internal" | sudo tee -a /etc/hosts >/dev/null
        HOSTS_CHANGED=true
    else
        echo "✅ host.k3d.internal already present in /etc/hosts"
        HOSTS_CHANGED=false
    fi
else
    echo "⚠️  Could not determine k3d network gateway IP — host.k3d.internal not wired. Image pulls from the workflow-plane registry may fail."
    HOSTS_CHANGED=false
fi

# registries.yaml: tells containerd host.k3d.internal:10082 is a plain-HTTP mirror, not HTTPS.
REGISTRIES_CHANGED=false
if [ ! -f /etc/rancher/k3s/registries.yaml ]; then
    echo "🔧 Copying registry mirror config from ${SERVER_CONTAINER}..."
    if docker cp "${SERVER_CONTAINER}:/etc/rancher/k3s/registries.yaml" /tmp/k3d-registries.yaml 2>/dev/null; then
        sudo mkdir -p /etc/rancher/k3s
        sudo cp /tmp/k3d-registries.yaml /etc/rancher/k3s/registries.yaml
        rm -f /tmp/k3d-registries.yaml
        REGISTRIES_CHANGED=true
    else
        echo "⚠️  Server has no registries.yaml to copy — skipping"
    fi
else
    echo "✅ /etc/rancher/k3s/registries.yaml already present"
fi

if [ "${HOSTS_CHANGED:-false}" = "true" ] || [ "$REGISTRIES_CHANGED" = "true" ]; then
    echo "🔄 Restarting k3s-agent to pick up registries.yaml..."
    sudo systemctl restart k3s-agent
    kubectl wait --context "$CLUSTER_CONTEXT" --for=condition=Ready "node/${THIS_NODE}" --timeout=120s
fi

# --- 4. Hand off to the production Kata installer, targeting this node ---
# install-kata.sh detects k3s and tells kata-deploy's chart to wire k3s's containerd.
echo ""
echo "🚀 Installing Kata on ${THIS_NODE} via install-kata.sh..."
KATA_NODES="${THIS_NODE}" bash "$SCRIPT_DIR/install-kata.sh"

echo ""
echo "✅ Bare k3s node '${THIS_NODE}' joined and Kata-ready."
echo ""
echo "Next steps:"
echo "  1. Create a Kata environment (ISOLATION_TIER=kata ... bash ../scripts/add-environment.sh)."
echo "  2. Deploy or promote an agent to that environment."
echo "  3. Verify: kubectl get pod <pod> -n <ns> -o wide                          # NODE == ${THIS_NODE}"
echo "             kubectl get pod <pod> -n <ns> -o jsonpath='{.spec.runtimeClassName}'  # kata-qemu"
