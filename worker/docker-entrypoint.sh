#!/bin/bash
set -e

CLUSTER_ADDRESS="${CLUSTER_ADDRESS:-cluster_manager}"
CLUSTER_PORT="${CLUSTER_PORT:-10100}"
MQTT_URL="${MQTT_URL:-mqtt}"
MQTT_PORT="${MQTT_PORT:-10003}"

# The worker runs in its own compose project, so there is no depends_on
# ordering with the cluster stack - wait for the cluster manager instead.
echo "[oakestra] Waiting for cluster manager at ${CLUSTER_ADDRESS}:${CLUSTER_PORT}..."
for i in $(seq 60); do
    (exec 3<>"/dev/tcp/${CLUSTER_ADDRESS}/${CLUSTER_PORT}") 2>/dev/null && break
    sleep 2
done
if ! (exec 3<>"/dev/tcp/${CLUSTER_ADDRESS}/${CLUSTER_PORT}") 2>/dev/null; then
    echo "[oakestra] ERROR: cluster manager not reachable after 120s" >&2
    exit 1
fi
echo "[oakestra] Cluster manager reachable."

# Clean up state from a previous run. When nodeengined crashes the container
# restarts with the old writable layer: stale pid files make daemons wait for
# processes that are not there, and the leftover containerd socket file makes
# a plain existence check pass before the new containerd actually listens.
rm -f /var/run/docker.pid /var/run/docker/containerd/containerd.pid \
      /run/containerd/containerd.pid \
      /run/containerd/containerd.sock /run/containerd/containerd.sock.ttrpc

# NodeEngine talks to containerd at /run/containerd/containerd.sock, so run a
# standalone containerd (the docker:dind base image ships the binary). The
# generated default config also lets NodeEngine probe available OCI runtimes.
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
containerd >/var/log/containerd.log 2>&1 &

# Wait until containerd answers an API call - the socket file alone is not
# enough, NodeEngine permanently disables its container runtime (and later
# panics on deploy) if the dial fails once at startup.
echo "[oakestra] Waiting for containerd..."
for i in $(seq 30); do
    ctr version >/dev/null 2>&1 && break
    sleep 1
done
if ! ctr version >/dev/null 2>&1; then
    echo "[oakestra] ERROR: containerd not answering after 30s" >&2
    exit 1
fi
echo "[oakestra] containerd ready."

# NodeEngine uses a single ClusterAddress for both the HTTP handshake (port
# CLUSTER_PORT) and MQTT (port MQTT_PORT). Since those are separate containers
# in compose, we forward both ports to localhost so NodeEngine can use a single
# address for both.
socat TCP-LISTEN:${CLUSTER_PORT},fork,reuseaddr TCP:${CLUSTER_ADDRESS}:${CLUSTER_PORT} &
socat TCP-LISTEN:${MQTT_PORT},fork,reuseaddr TCP:${MQTT_URL}:${MQTT_PORT} &

# Write NodeEngine config
mkdir -p /etc/oakestra /var/log/oakestra
NodeEngine config default
NodeEngine config cluster localhost --clusterPort "${CLUSTER_PORT}"
# Use manual socket mode so NodeEngine does not try to invoke systemctl
NodeEngine config network manual

# Write NetManager config
mkdir -p /etc/netmanager
cat > /etc/netmanager/netcfg.json <<EOF
{
  "NodePublicAddress": "0.0.0.0",
  "NodePublicPort": "50103",
  "ClusterUrl": "${MQTT_URL}",
  "ClusterMqttPort": "${MQTT_PORT}",
  "DefaultInterface": "",
  "Debug": false,
  "PublicIPNetworking": false,
  "MqttCert": "",
  "MqttKey": ""
}
EOF

# Start NetManager - output goes to stdout so failures are visible in compose logs
NetManager &
NM_PID=$!

echo "[oakestra] Waiting for NetManager socket (pid ${NM_PID})..."
for i in $(seq 30); do
    if ! kill -0 "${NM_PID}" 2>/dev/null; then
        echo "[oakestra] ERROR: NetManager process exited early (exit code: $?)" >&2
        wait "${NM_PID}"; echo "[oakestra] NetManager exit status: $?" >&2
        exit 1
    fi
    [ -S /etc/netmanager/netmanager.sock ] && break
    sleep 1
done
if [ ! -S /etc/netmanager/netmanager.sock ]; then
    echo "[oakestra] ERROR: NetManager socket not ready after 30s" >&2
    exit 1
fi
echo "[oakestra] NetManager ready."

exec nodeengined
