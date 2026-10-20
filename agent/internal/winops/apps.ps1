<#
.SYNOPSIS
  Installs applications for servermgr, silently.
.DESCRIPTION
  Reads a JSON list of applications (id, name, url, kind msi|exe|zip, args,
  signer and/or sha256, exe), downloads each over HTTPS and installs it only
  if its Authenticode signature is valid and from the expected publisher, or
  its SHA-256 matches. A zip (portable tools) is unpacked under
  Program Files\Tools, its program's folder put on PATH and given a Start
  menu shortcut; for a zip the signature checked is the program's. Used by Windows template builds (from the build CD)
  and by "Install applications" on a running VM (through the guest agent).
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Json,
    [string]$LogFile = 'C:\Windows\Temp\servermgr-apps.log'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function CheckSigner([string]$path, $a) {
    $sig = Get-AuthenticodeSignature -FilePath $path
    if ($sig.Status -ne 'Valid' -or $sig.SignerCertificate.Subject -notmatch $a.signer) {
        throw "signature is not valid or not from the expected publisher ($($sig.Status), $($sig.SignerCertificate.Subject))"
    }
}

# InstallZip unpacks a portable tool into Program Files\Tools\<id>, which
# only administrators can change.
function InstallZip([string]$zip, $a) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $tmp = Join-Path (Split-Path $zip) ('servermgr-unzip-' + $a.id)
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
    [IO.Compression.ZipFile]::ExtractToDirectory($zip, $tmp)
    # One top folder in the archive (ghidra_11.4_PUBLIC\...): use it.
    $top = @(Get-ChildItem -Path $tmp -Force)
    $src = if ($top.Count -eq 1 -and $top[0].PSIsContainer) { $top[0].FullName } else { $tmp }
    $main = $null
    if ($a.exe) {
        $main = Get-ChildItem -Path $src -Recurse -File -Filter $a.exe | Sort-Object { $_.FullName.Length } | Select-Object -First 1
        if (-not $main) { throw "$($a.exe) is not in the archive" }
        if ($a.signer) { CheckSigner $main.FullName $a }
    }
    $root = Join-Path $env:ProgramFiles 'Tools'
    New-Item -ItemType Directory -Force -Path $root | Out-Null
    $dest = Join-Path $root $a.id
    Remove-Item -Path $dest -Recurse -Force -ErrorAction SilentlyContinue
    Move-Item -Path $src -Destination $dest
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
    if ($main) {
        $target = $dest + $main.FullName.Substring($src.Length)
        $dir = Split-Path $target
        $path = [Environment]::GetEnvironmentVariable('Path', 'Machine')
        if (($path -split ';') -notcontains $dir) {
            [Environment]::SetEnvironmentVariable('Path', $path.TrimEnd(';') + ';' + $dir, 'Machine')
        }
        $menu = Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs\Tools'
        New-Item -ItemType Directory -Force -Path $menu | Out-Null
        $lnk = (New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path $menu ($a.name + '.lnk')))
        $lnk.TargetPath = $target
        $lnk.WorkingDirectory = $dir
        $lnk.Save()
    }
}

function Log([string]$msg) {
    $line = (Get-Date).ToString('HH:mm:ss') + ' ' + $msg
    Write-Host $line
    Add-Content -Path $LogFile -Value $line -Encoding UTF8
}

# Windows PowerShell 5.1 hands a JSON array on as one object: assign it,
# then foreach walks its items.
$apps = Get-Content -Raw -Path $Json | ConvertFrom-Json
$failed = 0
foreach ($a in $apps) {
    $ext = '.' + $(if ($a.kind -in 'msi', 'zip') { $a.kind } else { 'exe' })
    $file = Join-Path $env:TEMP ('servermgr-app-' + $a.id + $ext)
    try {
        if ($a.url -notmatch '^https://') { throw 'not an https URL' }
        if (-not $a.signer -and -not $a.sha256) { throw 'nothing to verify the download with' }
        Log "$($a.name): downloading"
        for ($i = 1; ; $i++) {
            try { Invoke-WebRequest -Uri $a.url -OutFile $file -UseBasicParsing; break }
            catch { if ($i -ge 5) { throw }; Log "$($a.name): download failed, retrying: $($_.Exception.Message)"; Start-Sleep -Seconds 10 }
        }
        if ($a.sha256) {
            $hash = (Get-FileHash -Algorithm SHA256 -Path $file).Hash
            if ($hash -ne ([string]$a.sha256).ToUpper()) { throw "SHA-256 does not match ($hash)" }
        }
        if ($a.signer -and $a.kind -ne 'zip') { CheckSigner $file $a }
        Log "$($a.name): installing"
        $extra = @($a.args | Where-Object { $_ })
        if ($a.kind -eq 'zip') {
            InstallZip $file $a
            $p = @{ ExitCode = 0 }
        } elseif ($a.kind -eq 'msi') {
            $p = Start-Process msiexec.exe -ArgumentList (@('/i', "`"$file`"", '/qn', '/norestart') + $extra) -Wait -PassThru
        } elseif ($extra.Count) {
            $p = Start-Process -FilePath $file -ArgumentList $extra -Wait -PassThru
        } else {
            $p = Start-Process -FilePath $file -Wait -PassThru
        }
        if ($p.ExitCode -notin 0, 3010, 1641) { throw "the installer exited with code $($p.ExitCode)" }
        Log "$($a.name): installed"
    } catch {
        $failed++
        Log "$($a.name): FAILED: $($_.Exception.Message)"
    } finally {
        Remove-Item -Path $file -Force -ErrorAction SilentlyContinue
    }
}
Log "SERVERMGR-APPS done: $(@($apps).Count - $failed) installed, $failed failed"
if ($failed) { exit 1 }
