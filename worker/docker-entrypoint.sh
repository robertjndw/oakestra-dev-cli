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

# Start NetManager under its own supervised restart loop, same shape as
# nodeengined's below - `oak-dev reload netmanager` sends it a TERM (pkill -x
# NetManager) to pick up a freshly cross-compiled binary from the /oak-bin
# mount (see compose/override-live-netmanager.yml) without recreating this
# container. `oak-dev debug netmanager` sets OAK_DEV_DEBUG_NETMANAGER=1 (see
# compose/override-debug-netmanager.yml) to run it under Delve instead -
# listening on :2346, not :2345, since nodeengined's own debug listener below
# already claims that port and both can be debugged at once.
#
# Runs as a background job of this script (PID 1) rather than inline, so PID 1
# can go on to start nodeengined; its own `trap` is what lets the outer trap
# (below) propagate a shutdown signal down to whichever NetManager/dlv process
# is currently running inside it.
supervise_netmanager() {
    # `|| true` on each step: under `set -e`, kill/wait failing partway
    # through this trap (e.g. the child already gone) would abort the trap
    # itself before reaching `exit 0`.
    trap 'kill -TERM "$NM_PID" 2>/dev/null || true; wait "$NM_PID" 2>/dev/null || true; exit 0' TERM INT
    while true; do
        if [ -n "${OAK_DEV_DEBUG_NETMANAGER:-}" ]; then
            dlv exec --headless --listen=:2346 --api-version=2 --accept-multiclient --continue /oak-bin/NetManager &
        else
            NetManager &
        fi
        NM_PID=$!
        # `if wait ...` rather than a bare `wait` - under `set -e`, a bare
        # `wait` returning the child's non-zero exit status would kill this
        # whole script (and so the container) the moment NetManager exits for
        # ANY reason, defeating the point of the loop. `set -e` exempts
        # commands used as an if/while condition, which is why this form
        # survives it and a bare `wait "$NM_PID"` does not.
        if wait "$NM_PID"; then code=0; else code=$?; fi
        echo "[oakestra] NetManager exited (code $code) - relaunching"
        sleep 1
    done
}
supervise_netmanager &
NM_SUP_PID=$!

echo "[oakestra] Waiting for NetManager socket (supervisor pid ${NM_SUP_PID})..."
for i in $(seq 30); do
    if ! kill -0 "${NM_SUP_PID}" 2>/dev/null; then
        echo "[oakestra] ERROR: NetManager supervisor exited early" >&2
        exit 1
    fi
    [ -S /etc/netmanager/netmanager.sock ] && break
    sleep 1
done
if [ ! -S /etc/netmanager/netmanager.sock ]; then
    echo "[oakestra] ERROR: NetManager socket not ready after 30s (it may be crash-looping - check \`oak-dev logs worker\`)" >&2
    exit 1
fi
echo "[oakestra] NetManager ready."

# Supervised rather than exec'd: `oak-dev worker reload` sends nodeengined a
# TERM (docker compose exec worker pkill nodeengined) to pick up a freshly
# cross-compiled binary from the /oak-bin mount (see compose/override-live-
# worker.yml) without recreating this container. Recreating would mint a new
# node ID on the next cluster handshake and strand any scheduled instances in
# NODE_SCHEDULED forever (see CLAUDE.md). The trap below still lets a normal
# `docker compose stop`/`down` (SIGTERM to PID 1) shut down promptly, and
# forwards it to the NetManager supervisor above too. `|| true` on each step
# for the same set -e reason as the trap in supervise_netmanager above.
trap 'kill -TERM "$NE_PID" "$NM_SUP_PID" 2>/dev/null || true; wait "$NE_PID" "$NM_SUP_PID" 2>/dev/null || true; exit 0' TERM INT

# `oak-dev debug nodeengine` sets OAK_DEV_DEBUG=1 (see
# compose/override-debug-worker.yml) to run the daemon under Delve instead of
# directly - everything above (containerd, NetManager, port forwarding) still
# needs to happen first, so this can't just be a different compose entrypoint.
while true; do
    if [ -n "${OAK_DEV_DEBUG:-}" ]; then
        dlv exec --headless --listen=:2345 --api-version=2 --accept-multiclient --continue /oak-bin/nodeengined &
    else
        nodeengined &
    fi
    NE_PID=$!
    # See the comment on the equivalent NetManager loop above: this must not
    # be a bare `wait` under `set -e`, or a crash (or even a normal
    # `pkill nodeengined` reload) would exit the whole script instead of
    # being caught and relaunched here.
    if wait "$NE_PID"; then code=0; else code=$?; fi
    echo "[oakestra] nodeengined exited (code $code) - relaunching"
    sleep 1
done
