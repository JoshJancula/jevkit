# Install jevkit for the current Windows user. Set $env:JEVKIT_VERSION to pin.
$ErrorActionPreference = 'Stop'
$repo = 'JoshJancula/jevkit'
$arch = if ([Environment]::Is64BitOperatingSystem) { if ($env:PROCESSOR_ARCHITECTURE -match 'ARM') { 'arm64' } else { 'amd64' } } else { throw 'jevkit requires 64-bit Windows' }
$version = if ($env:JEVKIT_VERSION) { $env:JEVKIT_VERSION.TrimStart('v') } else { (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name.TrimStart('v') }
$archive = "jevkit_${version}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/v$version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("jevkit-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Invoke-WebRequest "$base/$archive" -OutFile (Join-Path $tmp $archive)
  Invoke-WebRequest "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')
  $expected = ((Get-Content (Join-Path $tmp 'checksums.txt')) | Where-Object { $_ -match [regex]::Escape($archive) } | Select-Object -First 1).Split()[0]
  if (-not $expected -or (Get-FileHash (Join-Path $tmp $archive) -Algorithm SHA256).Hash.ToLower() -ne $expected.ToLower()) { throw 'checksum verification failed' }
  Expand-Archive (Join-Path $tmp $archive) -DestinationPath $tmp -Force
  $dest = if ($env:JEVKIT_INSTALL_DIR) { $env:JEVKIT_INSTALL_DIR } else { Join-Path $HOME '.local\bin' }
  New-Item -ItemType Directory -Force -Path $dest | Out-Null
  Copy-Item (Get-ChildItem $tmp -Recurse -Filter jevkit.exe | Select-Object -First 1).FullName (Join-Path $dest 'jevkit.exe') -Force
  Write-Host "installed $dest\jevkit.exe"
  Write-Host 'next: jevkit key set; jevkit install <agent>'
} finally { Remove-Item -Recurse -Force $tmp }
