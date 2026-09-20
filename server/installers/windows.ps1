$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$architecture = $env:PROCESSOR_ARCHITEW6432
if ([string]::IsNullOrEmpty($architecture)) {
    $architecture = $env:PROCESSOR_ARCHITECTURE
}
if ($architecture -ne 'AMD64') {
    throw "Hex supports Windows x86-64. Detected architecture: $architecture"
}
if ([string]::IsNullOrEmpty($env:LOCALAPPDATA)) {
    throw 'LOCALAPPDATA is required to install Hex for the current user.'
}

[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$releaseURL = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('{{.ReleaseURL}}'))
$temporary = Join-Path ([IO.Path]::GetTempPath()) ('hex-install-' + [Guid]::NewGuid().ToString('N'))
$binDirectory = Join-Path $env:LOCALAPPDATA 'Hex\bin'
New-Item -ItemType Directory -Path $temporary | Out-Null

try {
    Write-Host 'Downloading the latest Hex CLI for Windows x86-64...'
    $download = Join-Path $temporary 'hex.exe'
    $checksumFile = Join-Path $temporary 'SHA256SUMS'
    Invoke-WebRequest -UseBasicParsing -Uri "$releaseURL/SHA256SUMS" -OutFile $checksumFile
    Invoke-WebRequest -UseBasicParsing -Uri "$releaseURL/hex-windows-amd64.exe" -OutFile $download
    $checksums = [IO.File]::ReadAllText($checksumFile)
    $entries = [regex]::Matches($checksums, '(?m)^([a-fA-F0-9]{64})\s+hex-windows-amd64\.exe\r?$')
    if ($entries.Count -ne 1) {
        throw 'The release has no unique SHA-256 checksum for the Windows binary.'
    }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $download).Hash
    if ($actual -ne $entries[0].Groups[1].Value) {
        throw 'Checksum mismatch. No binary was installed. Download and run the installer again.'
    }

    $configuration = Join-Path $temporary 'platform.json'
    [IO.File]::WriteAllBytes($configuration, [Convert]::FromBase64String('{{.Connection}}'))
    New-Item -ItemType Directory -Force -Path $binDirectory | Out-Null
    $binary = Join-Path $binDirectory 'hex.exe'
    Copy-Item -LiteralPath $download -Destination $binary -Force
    & $binary setup --file $configuration
    if ($LASTEXITCODE -ne 0) {
        throw 'Hex could not import the platform configuration. Resolve the reported error and rerun the installer.'
    }

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = @($userPath -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($entries -notcontains $binDirectory) {
        [Environment]::SetEnvironmentVariable('Path', ((@($binDirectory) + $entries) -join ';'), 'User')
    }
    $env:Path = "$binDirectory;$env:Path"
    Write-Host "`nHex is installed at $binary and connected to your platform."
    Write-Host 'Open a new terminal, then run: hex init my-app'
} finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force
}
