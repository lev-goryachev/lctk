param(
    [string]$Version = "0.1.2",
    [string]$TemplateVersion = "0.1.1",
    [string]$TemplateKeyID = "lctk-release-v1",
    [string]$TemplatePublicKey = "rSVhZIN82jXEG04WGzc9lAu5bszLjs//cSEO0bmJY8I="
)

$ErrorActionPreference = "Stop"

# The local RC is deliberately assembled only beneath the repository's ignored
# artifact directory. The guard keeps cleanup from ever following a changed
# working directory into an unrelated path.
$repository = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$artifactRoot = Join-Path $repository ".artifacts\local-rc"
$expectedPrefix = (Join-Path $repository ".artifacts") + [IO.Path]::DirectorySeparatorChar
$resolvedArtifactRoot = [IO.Path]::GetFullPath($artifactRoot)
if (-not $resolvedArtifactRoot.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Local RC artifact directory escaped the repository."
}
if (Test-Path -LiteralPath $resolvedArtifactRoot) {
    Remove-Item -LiteralPath $resolvedArtifactRoot -Recurse -Force
}
$package = Join-Path $resolvedArtifactRoot "package"
New-Item -ItemType Directory -Force -Path $package | Out-Null

$seed = New-Object byte[] 32
$random = [Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $random.GetBytes($seed)
}
finally {
    $random.Dispose()
}
$env:LCTK_RELEASE_ED25519_PRIVATE_KEY = [Convert]::ToBase64String($seed)
try {
    $publicKey = (& go run ./cmd/release-manifest --print-public-key).Trim()
    if (-not $publicKey) {
        throw "The local release public key was not generated."
    }
    $commit = (& git rev-parse HEAD).Trim()
    $publishedAt = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    $template = Join-Path $resolvedArtifactRoot "template-manifest.json"
    $templateURL = "https://github.com/lev-goryachev/lctk/releases/download/v$TemplateVersion/release-manifest.json"
    Invoke-WebRequest -UseBasicParsing -Uri $templateURL -OutFile $template

    $trust = "-s -w -X github.com/lev-goryachev/lctk/internal/buildinfo.Version=$Version -X github.com/lev-goryachev/lctk/internal/buildinfo.Commit=$commit -X github.com/lev-goryachev/lctk/internal/buildinfo.Date=$publishedAt -X github.com/lev-goryachev/lctk/internal/releasebundle.TrustedKeyID=lctk-local-rc -X github.com/lev-goryachev/lctk/internal/releasebundle.TrustedPublicKey=$publicKey"
    go build -trimpath -ldflags $trust -o (Join-Path $package "lctk-core.exe") ./cmd/lctk
    go build -trimpath -ldflags "$trust -H=windowsgui" -o (Join-Path $package "lctk-setup.exe") ./cmd/lctk-setup

    $core = Join-Path $package "lctk-core.exe"
    $coreHash = (Get-FileHash -Algorithm SHA256 $core).Hash.ToLowerInvariant()
    $coreBytes = (Get-Item -LiteralPath $core).Length
    go build -trimpath -ldflags "-s -w -X main.PackagedCoreSHA256=$coreHash -X main.PackagedCoreBytes=$coreBytes" -o (Join-Path $package "lctk.exe") ./cmd/lctk-launcher

    $manifest = Join-Path $package "release-manifest.json"
    go run ./cmd/release-manifest `
        --version $Version --commit $commit --published-at $publishedAt `
        --base-url "http://127.0.0.1:4466" --key-id "lctk-local-rc" `
        --template-envelope $template --template-key-id $TemplateKeyID --template-public-key $TemplatePublicKey `
        --artifact "lctk-core.exe,host-core,windows,amd64,$core" `
        --artifact "lctk.exe,host-launcher,windows,amd64,$package\lctk.exe" `
        --artifact "lctk-setup.exe,installer,windows,amd64,$package\lctk-setup.exe" `
        --output $manifest

    $payload = Join-Path $resolvedArtifactRoot "payload.zip"
    Compress-Archive -LiteralPath $core,(Join-Path $package "lctk.exe"),(Join-Path $package "lctk-setup.exe"),$manifest -DestinationPath $payload
    $outputs = @()
    foreach ($candidate in @(
        @{ Name = "LCTK-Setup-local-RC.exe"; Bootstrap = "bootstrap-setup.exe"; LinkerFlags = "-s -w -H=windowsgui" },
        @{ Name = "LCTK-Uninstall-local-RC.exe"; Bootstrap = "bootstrap-uninstall.exe"; LinkerFlags = "-s -w -H=windowsgui -X main.DefaultSetupMode=uninstall" }
    )) {
        $bootstrap = Join-Path $resolvedArtifactRoot $candidate.Bootstrap
        go build -trimpath -ldflags $candidate.LinkerFlags -o $bootstrap ./cmd/lctk-local-setup
        $output = Join-Path $repository (".artifacts\" + $candidate.Name)
        $stream = [IO.File]::Open($output, [IO.FileMode]::Create, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try {
            foreach ($source in @($bootstrap, $payload)) {
                $input = [IO.File]::OpenRead($source)
                try {
                    $input.CopyTo($stream)
                }
                finally {
                    $input.Dispose()
                }
            }
            $stream.Flush($true)
        }
        finally {
            $stream.Dispose()
        }
        $outputs += Get-Item -LiteralPath $output
    }
    $outputs | Select-Object FullName,Length,@{Name="SHA256";Expression={(Get-FileHash -Algorithm SHA256 $_.FullName).Hash}}
}
finally {
    Remove-Item Env:LCTK_RELEASE_ED25519_PRIVATE_KEY -ErrorAction SilentlyContinue
}
