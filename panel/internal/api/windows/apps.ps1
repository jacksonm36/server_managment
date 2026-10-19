<#
.SYNOPSIS
  Installs applications for servermgr, silently.
.DESCRIPTION
  Reads a JSON list of applications (id, name, url, kind msi|exe, args,
  signer and/or sha256), downloads each over HTTPS and installs it only if
  its Authenticode signature is valid and from the expected publisher, or
  its SHA-256 matches. Used by Windows template builds (from the build CD)
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
    $ext = if ($a.kind -eq 'msi') { '.msi' } else { '.exe' }
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
        if ($a.signer) {
            $sig = Get-AuthenticodeSignature -FilePath $file
            if ($sig.Status -ne 'Valid' -or $sig.SignerCertificate.Subject -notmatch $a.signer) {
                throw "signature is not valid or not from the expected publisher ($($sig.Status), $($sig.SignerCertificate.Subject))"
            }
        }
        Log "$($a.name): installing"
        $extra = @($a.args | Where-Object { $_ })
        if ($a.kind -eq 'msi') {
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
