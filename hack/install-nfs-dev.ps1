# Dedicated kind fixture: static PVs use the NFS node plugin without a provisioner.
$ErrorActionPreference='Stop'
$projectRoot=Split-Path -Parent $PSScriptRoot
Push-Location $projectRoot
try {
    $env:KUBECONFIG=Join-Path $projectRoot '.smartdrain.kubeconfig'
    function Run {param([scriptblock]$Command) & $Command; if($LASTEXITCODE -ne 0){throw "Failed: $Command"}}
    Run {kubectl --context kind-smartdrain-dev get nodes}
    $images=@(
        @('registry.k8s.io/sig-storage/nfsplugin:v4.12.1','8835d96f4389cb1ea8996b4bf9cad8af960f52508c9b8372a877c6656fd92264'),
        @('registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.15.0','1ca23bdbb1504e41233d527e826d02dac98571c45a6cd74c5fb28be485d680fa'),
        @('registry.k8s.io/sig-storage/livenessprobe:v2.17.0','e8983e659df16bda5bd8d2900651e1236b1c1bd0c96c8600aec736e7eabdb1e4')
    )
    foreach($pair in $images){Run {docker pull --platform linux/amd64 "$($pair[0])@sha256:$($pair[1])"};Run {docker tag "$($pair[0])@sha256:$($pair[1])" $pair[0]}}
    New-Item -ItemType Directory -Force _work/nfs | Out-Null
    Run {docker image save --platform linux/amd64 -o _work/nfs/images.tar $images[0][0] $images[1][0] $images[2][0]}
    foreach($node in @('smartdrain-dev-control-plane','smartdrain-dev-worker','smartdrain-dev-worker2','smartdrain-dev-worker3')){
        # Import through /root, outside kind's /tmp mount, with an explicit platform.
        Run {docker cp _work/nfs/images.tar "${node}:/root/smartdrain-nfs.tar"}
        Run {docker exec $node ctr -n k8s.io images import --platform linux/amd64 --digests /root/smartdrain-nfs.tar}
        foreach($pair in $images){$repository=$pair[0].Substring(0,$pair[0].LastIndexOf(':'));Run {docker exec $node ctr -n k8s.io images tag --force $pair[0] "${repository}@sha256:$($pair[1])"}}
    }
    $serverImage='itsthenetwork/nfs-server-alpine@sha256:7fa99ae65c23c5af87dd4300e543a86b119ed15ba61422444207efc7abd0ba20'
    $existing=docker ps -a --filter 'name=^/smartdrain-nfs$' --format '{{.Names}}'
    if(!$existing){Run {docker run -d --name smartdrain-nfs --network kind --privileged -e SHARED_DIRECTORY=/exports -v smartdrain-nfs-data:/exports $serverImage}}
    else {
        $actualImage=docker inspect smartdrain-nfs --format '{{.Config.Image}}'
        if($actualImage -ne $serverImage){throw 'Existing smartdrain-nfs container uses a different image'}
        Run {docker start smartdrain-nfs}
    }
    Run {kubectl --context kind-smartdrain-dev apply -k test/fixtures/nfs}
    Run {kubectl --context kind-smartdrain-dev -n kube-system rollout status ds/csi-nfs-node --timeout=120s}
    $address=docker inspect smartdrain-nfs --format '{{.NetworkSettings.Networks.kind.IPAddress}}'
    Write-Output "NFS server for SMARTDRAIN_NFS_SERVER: $address"
} finally {Pop-Location}
