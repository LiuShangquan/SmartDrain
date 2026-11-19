param([switch]$Cluster)
$ErrorActionPreference='Stop'
$projectRoot=Split-Path -Parent $PSScriptRoot
Push-Location $projectRoot
try {
    $env:KUBECONFIG=Join-Path $projectRoot '.smartdrain.kubeconfig'
    go test -count=1 ./...
    if($LASTEXITCODE -ne 0){throw 'Unit tests failed'}
    go vet ./...
    if($LASTEXITCODE -ne 0){throw 'go vet failed'}
    if($Cluster){
        $env:SMARTDRAIN_API_TESTS='1'
        go test -v -count=1 ./test/integration
        if($LASTEXITCODE -ne 0){throw 'API tests failed'}
        $env:SMARTDRAIN_E2E='1'
        go test -v -count=1 -timeout=20m ./test/e2e
        if($LASTEXITCODE -ne 0){throw 'Cluster tests failed'}
    }
} finally { Pop-Location }
