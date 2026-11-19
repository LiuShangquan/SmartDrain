#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export KUBECONFIG="$PWD/.smartdrain.kubeconfig"
if [[ "${1:-}" == --create-cluster ]]; then
  kind create cluster --name smartdrain-dev --config test/e2e/kind.yaml \
    --image kindest/node:v1.35.0@sha256:4613778f3cfcd10e615029370f5786704559103cf27bef934597ba562b269661 \
    --kubeconfig "$KUBECONFIG" --wait 120s
fi
kubectl --context kind-smartdrain-dev get nodes
make generate manifests
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/manager-linux-amd64 ./cmd
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/testapp-linux-amd64 ./test/app
docker build -f Dockerfile.runtime -t smartdrain:dev .
docker build -f test/app/Dockerfile -t smartdrain-testapp:dev .
kind load docker-image smartdrain:dev smartdrain-testapp:dev --name smartdrain-dev
kubectl --context kind-smartdrain-dev apply -k config/default
kubectl --context kind-smartdrain-dev -n smartdrain-system rollout restart deployment/smartdrain-controller-manager
kubectl --context kind-smartdrain-dev -n smartdrain-system rollout status deployment/smartdrain-controller-manager --timeout=120s
