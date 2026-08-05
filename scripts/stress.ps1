param(
    [ValidatePattern('^\d+(,\d+)*$')]
    [string]$Counts = '1000,10000,100000,1000000',
    [ValidateSet('semantic', 'exact')]
    [string]$Adapter = 'semantic'
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$artifactRoot = Join-Path $root '.artifacts\stress'
$resolvedRoot = [IO.Path]::GetFullPath($root)
$resolvedArtifacts = [IO.Path]::GetFullPath($artifactRoot)
if (-not $resolvedArtifacts.StartsWith($resolvedRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Stress artifacts escaped the repository root.'
}

New-Item -ItemType Directory -Force $resolvedArtifacts | Out-Null
$mount = $resolvedArtifacts.Replace('\', '/')
$runID = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssfffffffZ')
$containerRoot = "/evidence/runs/$runID/$Adapter"
$evidencePath = Join-Path $resolvedArtifacts "$Adapter-curves-$runID.jsonl"
$volumeName = "lctk-stress-$($runID.ToLowerInvariant())-$Adapter"

docker build --target stress --tag lctk-semantic-stress:local (Join-Path $root 'images\code-intel')
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$createdVolume = docker volume create --label tech.lctk.stress=true $volumeName
if ($LASTEXITCODE -ne 0 -or $createdVolume -ne $volumeName) { throw 'Could not create the isolated stress state volume.' }

$runExit = 1
try {
    $entrypoint = "/usr/local/bin/$Adapter-stress"
    $arguments = @(
        'run', '--rm', '--network', 'none', '--entrypoint', $entrypoint,
        '--mount', "type=volume,source=$volumeName,target=/state",
        '--mount', "type=bind,source=$mount,target=/evidence",
        'lctk-semantic-stress:local'
    )
    if ($Adapter -eq 'semantic') {
        $arguments += @('--root', '/state/semantic', '--counts', $Counts)
    }
    else {
        $arguments += @('--corpus-root', $containerRoot, '--state-root', '/state/exact', '--counts', $Counts)
    }
    & docker @arguments | Tee-Object -FilePath $evidencePath
    $runExit = $LASTEXITCODE
}
finally {
    docker volume rm --force $volumeName | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Could not remove stress state volume $volumeName." }
}
if ($runExit -ne 0) { exit $runExit }
