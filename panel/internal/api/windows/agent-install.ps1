# Installs and enrolls the servermgr agent on Windows, run by the panel as
# SYSTEM through the QEMU guest agent. $P holds panel (URL), sha256 (of the
# agent build) and ca (the SHA-256 of the panel CA certificate). The
# one-time join token is in a file only SYSTEM and Administrators can read;
# it is deleted at once and handed to the agent in its environment, never
# on a command line.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$work = 'C:\Windows\servermgr\enroll'
$tokenFile = Join-Path $work 'join-token'
$token = (Get-Content -Raw -Path $tokenFile).Trim()
Remove-Item -Path $tokenFile -Force

# The panel's HTTPS certificate is issued by its own CA, which Windows does
# not know. Trust exactly that CA: the chain the panel sends must end in the
# certificate whose SHA-256 the panel gave, with nothing wrong but the root
# being unknown to Windows. The host name is not checked (the panel may be
# reached by an address missing from its certificate), as the agent does.
Add-Type -TypeDefinition @'
using System;
using System.Net;
using System.Net.Security;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
public static class ServermgrPin {
    public static string CA;
    public static bool Check(object sender, X509Certificate cert, X509Chain chain, SslPolicyErrors errors) {
        if (chain == null || chain.ChainElements.Count < 2) return false;
        if ((errors & SslPolicyErrors.RemoteCertificateNotAvailable) != 0) return false;
        X509Certificate2 root = chain.ChainElements[chain.ChainElements.Count - 1].Certificate;
        string sum;
        using (SHA256 h = SHA256.Create()) { sum = BitConverter.ToString(h.ComputeHash(root.RawData)).Replace("-", ""); }
        if (!String.Equals(sum, CA, StringComparison.OrdinalIgnoreCase)) return false;
        foreach (X509ChainStatus s in chain.ChainStatus) {
            if (s.Status != X509ChainStatusFlags.NoError && s.Status != X509ChainStatusFlags.UntrustedRoot) return false;
        }
        return true;
    }
}
'@
[ServermgrPin]::CA = [string]$P.ca

# Program Files: only administrators can change the agent's binary.
$bin = Join-Path $env:ProgramFiles 'servermgr-agent'
New-Item -ItemType Directory -Force -Path $bin | Out-Null
$exe = Join-Path $bin 'servermgr-agent.exe'
$tmp = Join-Path $work 'servermgr-agent.download'

[Net.ServicePointManager]::ServerCertificateValidationCallback = [Net.Security.RemoteCertificateValidationCallback][ServermgrPin]::Check
try {
    Invoke-WebRequest -UseBasicParsing -Uri ($P.panel + '/download/servermgr-agent-windows-amd64.exe') -OutFile $tmp
} finally {
    [Net.ServicePointManager]::ServerCertificateValidationCallback = $null
}
# And the file must be the very build the panel has.
$hash = (Get-FileHash -Algorithm SHA256 -Path $tmp).Hash
if ($hash -ne ([string]$P.sha256).ToUpper()) {
    Remove-Item -Path $tmp -Force
    throw "the downloaded agent's SHA-256 ($hash) is not the panel's; refusing to run it"
}

$svc = Get-Service -Name servermgr-agent -ErrorAction SilentlyContinue
if ($svc) { Stop-Service -Name servermgr-agent -Force }
Move-Item -Path $tmp -Destination $exe -Force

$env:SM_JOIN_TOKEN = $token
try {
    $out = & $exe enroll --panel $P.panel --ca-sha256 $P.ca 2>&1
    if ($LASTEXITCODE -ne 0) { throw "enroll failed: $out" }
} finally {
    Remove-Item -Path Env:SM_JOIN_TOKEN -ErrorAction SilentlyContinue
}

if (-not $svc) {
    New-Service -Name servermgr-agent -DisplayName 'servermgr agent' -Description 'Connects this server to the servermgr panel.' `
        -BinaryPathName ('"' + $exe + '" run') -StartupType Automatic | Out-Null
}
sc.exe failure servermgr-agent reset= 86400 actions= restart/5000/restart/10000/restart/60000 | Out-Null
Start-Service -Name servermgr-agent
"$out"
