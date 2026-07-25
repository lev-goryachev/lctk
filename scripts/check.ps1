$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $goFiles = Get-ChildItem -Path 'cmd', 'internal' -Recurse -Filter '*.go' |
        ForEach-Object FullName
    $unformatted = & gofmt -l $goFiles
    if ($unformatted) {
        Write-Error "Run gofmt on:`n$($unformatted -join "`n")"
    }

    & go vet ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    & go test -cover ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
finally {
    Pop-Location
}
