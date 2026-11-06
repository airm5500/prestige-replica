#!/usr/bin/env bash
# Construit l'interface, l'intègre au Control Plane et compile l'agent.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
VERSION=${VERSION:-0.1.0-dev}

(cd "$ROOT/web" && npm ci && npm run build)
RES="$ROOT/control-plane/src/main/resources/META-INF/resources"
rm -rf "$RES" && mkdir -p "$RES" && cp -r "$ROOT/web/dist/." "$RES/"

(cd "$ROOT/control-plane" && mvn -B -q package ${SKIP_TESTS:+-DskipTests})

mkdir -p "$ROOT/dist"
for target in linux/amd64 linux/arm64 windows/amd64; do
  os=${target%/*}; arch=${target#*/}; ext=""; [ "$os" = windows ] && ext=".exe"
  (cd "$ROOT/agent" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
     -ldflags "-s -w -X main.Version=$VERSION" -o "$ROOT/dist/prm-agent-$os-$arch$ext" ./cmd/prm-agent)
done
(cd "$ROOT/dist" && sha256sum prm-agent-* > SHA256SUMS)
echo "Control Plane : control-plane/target/quarkus-app/  —  agents : dist/"
