param(
    [string[]]$Nodes,
    [string]$Selector,
    [ValidateSet('Guarded','DrainCompatible')][string]$Policy='Guarded',
    [switch]$PlanOnly,
    [switch]$Apply
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
if((!$Nodes -and !$Selector) -or ($Nodes -and $Selector)){throw 'Specify either -Nodes or -Selector'}
if($Selector){
    $selected=kubectl get nodes -l $Selector -o json | ConvertFrom-Json
    if($LASTEXITCODE -ne 0){throw 'Node selection failed'}
    $Nodes=@($selected.items | ForEach-Object {$_.metadata.name})
}
if(!$Nodes.Count){throw 'No nodes selected'}
$stamp=[DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
$mode='Execute';if($PlanOnly){$mode='PlanOnly'}
$items=@()
foreach($node in $Nodes){
    # Keep CR names short; the full target node name is stored in spec.
    $index=$items.Count
    $items+=@{apiVersion='maintenance.example.com/v1alpha1';kind='NodeMaintenance';metadata=@{name="maintenance-$stamp-$index"};spec=@{nodeName=$node;mode=$mode;executionPolicy=$Policy;drain=@{ignoreDaemonSets=$true}}}
}
$json=@{apiVersion='v1';kind='List';items=$items}|ConvertTo-Json -Depth 12
if($Apply){$json | kubectl apply -f -;if($LASTEXITCODE -ne 0){throw 'Task creation failed'}}else{$json}
