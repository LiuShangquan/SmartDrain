param([switch]$CreateCluster)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Push-Location $projectRoot
try {
    $env:KUBECONFIG = Join-Path $projectRoot '.smartdrain.kubeconfig'
    function Run { param([scriptblock]$Command) & $Command; if ($LASTEXITCODE -ne 0) { throw "Command failed: $Command" } }
    if ($CreateCluster) {
        Run { kind create cluster --name smartdrain-dev --config test/e2e/kind.yaml --image 'kindest/node:v1.35.0@sha256:4613778f3cfcd10e615029370f5786704559103cf27bef934597ba562b269661' --kubeconfig $env:KUBECONFIG --wait 120s }
    }
    Run { kubectl --context kind-smartdrain-dev get nodes }
    Run { go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.20.0 object:headerFile=hack/boilerplate.go.txt 'paths=./api/v1alpha1' }
    Run { go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.20.0 crd 'paths=./api/v1alpha1' output:crd:artifacts:config=config/crd/bases }
    Run { go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.20.0 rbac:roleName=smartdrain-manager-role 'paths=./internal/controller' output:rbac:artifacts:config=config/rbac }
    $oldOS, $oldArch, $oldCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
    try {
        $env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
        Run { go build -trimpath -o bin/manager-linux-amd64 ./cmd }
        Run { go build -trimpath -o bin/testapp-linux-amd64 ./test/app }
    } finally { $env:GOOS=$oldOS; $env:GOARCH=$oldArch; $env:CGO_ENABLED=$oldCGO }
    Run { docker build -f Dockerfile.runtime -t smartdrain:dev . }
    Run { docker build -f test/app/Dockerfile -t smartdrain-testapp:dev . }
    Run { kind load docker-image smartdrain:dev smartdrain-testapp:dev --name smartdrain-dev }
    Run { kubectl --context kind-smartdrain-dev apply -k config/default }
    Run { kubectl --context kind-smartdrain-dev -n smartdrain-system rollout restart deployment/smartdrain-controller-manager }
    Run { kubectl --context kind-smartdrain-dev -n smartdrain-system rollout status deployment/smartdrain-controller-manager --timeout=120s }
} finally { Pop-Location }
