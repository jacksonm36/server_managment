<#
.SYNOPSIS
  Prepares this Windows VM to become a servermgr cloud-init template on Proxmox.

.DESCRIPTION
  Installs the VirtIO drivers and QEMU guest agent, tunes Windows for a
  Proxmox VM, applies the Standard or Hardened profile, installs and
  configures Cloudbase-Init (cloud-init for Windows), cleans up and runs
  Sysprep, which shuts the VM down. Then use "Make Windows template" in
  servermgr.

  Standard: Windows defaults plus Proxmox tuning, Remote Desktop (with
  Network Level Authentication) and ping allowed.
  Hardened: also locks Windows down (firewall, TLS, SMB, NTLM, LSA,
  auditing, account lockout, Defender, automatic updates, unused services
  off). Remote Desktop stays reachable, with NLA and TLS only.

.PARAMETER Profile
  Standard or Hardened.
.PARAMETER Build
  Set by servermgr's unattended template build: no questions asked.
.PARAMETER NoSysprep
  Do everything except Sysprep (to inspect the VM first).
#>
[CmdletBinding()]
param(
    [ValidateSet('Standard', 'Hardened')][string]$Profile = 'Standard',
    [switch]$Build,
    [switch]$NoSysprep
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$LogFile = 'C:\Windows\Temp\servermgr-build.log'
$CloudbaseMsi = 'https://cloudbase.it/downloads/CloudbaseInitSetup_Stable_x64.msi'
$CloudbaseDir = 'C:\Program Files\Cloudbase Solutions\Cloudbase-Init'

function Log([string]$msg) {
    $line = (Get-Date).ToString('HH:mm:ss') + ' ' + $msg
    Write-Host $line
    Add-Content -Path $LogFile -Value $line -Encoding UTF8
}

function Try-Step([string]$what, [scriptblock]$do) {
    try { & $do } catch { Log "warning: $what failed: $($_.Exception.Message)" }
}

# Start-Process -Wait also waits for everything an installer leaves running
# (a tray app, an updater), which can be forever. Wait for the installer
# process itself, with a time limit.
function Invoke-Installer([string]$Path, [string[]]$Arguments, [int]$Minutes = 30) {
    $sp = @{ FilePath = $Path; PassThru = $true }
    if ($Arguments -and $Arguments.Count) { $sp.ArgumentList = $Arguments }
    $p = Start-Process @sp
    $null = $p.Handle  # keeps ExitCode readable after the wait (Windows PowerShell 5.1)
    if (-not $p.WaitForExit($Minutes * 60000)) { throw "the installer was still running after $Minutes minutes" }
    return $p.ExitCode
}

function Set-Reg([string]$path, [string]$name, $value, [string]$type = 'DWord') {
    if (-not (Test-Path $path)) { New-Item -Path $path -Force | Out-Null }
    New-ItemProperty -Path $path -Name $name -Value $value -PropertyType $type -Force | Out-Null
}

function Disable-Svc([string[]]$names) {
    foreach ($n in $names) {
        $s = Get-Service -Name $n -ErrorAction SilentlyContinue
        if ($s) {
            Try-Step "stopping $n" { Stop-Service -Name $n -Force -ErrorAction SilentlyContinue; Set-Service -Name $n -StartupType Disabled }
        }
    }
}

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this from an elevated PowerShell (Run as administrator).'
}
$os = Get-CimInstance Win32_OperatingSystem
$isServer = $os.ProductType -ne 1
Log "servermgr prepare: $($os.Caption) $($os.Version), profile $Profile"

# ---- Keep Windows Update and the Store from changing the image mid-way ----
# Sysprep refuses an image with servicing pending or Store apps updated
# for one user only.
$wu = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU'
Set-Reg $wu 'NoAutoUpdate' 1
Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\WindowsStore' 'AutoDownload' 2
Try-Step 'pausing Windows Update' { Stop-Service wuauserv -Force -ErrorAction SilentlyContinue }

# ---- VirtIO drivers, QEMU guest agent, SPICE agent ----
$virtio = Get-PSDrive -PSProvider FileSystem | ForEach-Object { $_.Root } |
    Where-Object { Test-Path (Join-Path $_ 'virtio-win-guest-tools.exe') } | Select-Object -First 1
if ($virtio) {
    Log "installing VirtIO drivers and the QEMU guest agent from $virtio"
    # Trust Red Hat's driver publisher so the install does not stop to ask.
    Try-Step 'trusting the driver publisher' {
        $cat = Get-ChildItem -Path $virtio -Recurse -Filter *.cat -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($cat) {
            $cert = (Get-AuthenticodeSignature $cat.FullName).SignerCertificate
            if ($cert) {
                $store = New-Object Security.Cryptography.X509Certificates.X509Store('TrustedPublisher', 'LocalMachine')
                $store.Open('ReadWrite'); $store.Add($cert); $store.Close()
            }
        }
    }
    # The guest tools leave the SPICE and guest agents running; waiting for
    # those as well would hang the build here.
    Try-Step 'VirtIO guest tools' {
        $code = Invoke-Installer (Join-Path $virtio 'virtio-win-guest-tools.exe') @('/install', '/quiet', '/norestart') 20
        Log "VirtIO guest tools: exit code $code"
    }
    Try-Step 'starting the guest agent' { Set-Service QEMU-GA -StartupType Automatic; Start-Service QEMU-GA }
} elseif (Get-Service QEMU-GA -ErrorAction SilentlyContinue) {
    Log 'VirtIO guest tools already installed'
} else {
    Log 'warning: no virtio-win CD found and no guest agent installed; attach the virtio-win ISO and run again for drivers and the agent'
}

# ---- Proxmox tuning ----
Log 'tuning Windows for a Proxmox VM'
Try-Step 'power plan' {
    powercfg /setactive 8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c | Out-Null   # High performance
    powercfg /change monitor-timeout-ac 0 | Out-Null
    powercfg /change standby-timeout-ac 0 | Out-Null
    powercfg /change disk-timeout-ac 0 | Out-Null
    powercfg /hibernate off | Out-Null     # also turns off Fast Startup
}
# TRIM: Proxmox gives the disk discard=on,ssd=1, so freed space goes back
# to thin storage and Windows re-trims instead of defragmenting.
Try-Step 'TRIM' { fsutil behavior set DisableDeleteNotify 0 | Out-Null }
# Prefetching does nothing for virtual disks but costs I/O.
Disable-Svc @('SysMain')
# No screen saver or lock timeout on the console; RDP sessions have their own.
Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\Control Panel\Desktop' 'ScreenSaveActive' '0' 'String'
# The clock comes from Proxmox (localtime=1, guest agent); keep NTP but
# let a big jump after a restore or migration correct itself.
Try-Step 'time sync' {
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Services\W32Time\Config' 'MaxPosPhaseCorrection' 0xFFFFFFFF
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Services\W32Time\Config' 'MaxNegPhaseCorrection' 0xFFFFFFFF
}
# Crash dumps small enough not to fill a template-sized disk.
Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\CrashControl' 'CrashDumpEnabled' 3
# No consumer apps and tips pulled in on first logon.
Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\CloudContent' 'DisableWindowsConsumerFeatures' 1
Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\CloudContent' 'DisableSoftLanding' 1
# Server Manager does not pop up at every logon.
if ($isServer) { Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\Server\ServerManager' 'DoNotOpenAtLogon' 1 }
# Device encryption would make every clone unreadable without its TPM key.
Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\BitLocker' 'PreventDeviceEncryption' 1

# ---- Remote Desktop (both profiles), always with NLA ----
Log 'enabling Remote Desktop with Network Level Authentication'
$ts = 'HKLM:\SYSTEM\CurrentControlSet\Control\Terminal Server'
Set-Reg $ts 'fDenyTSConnections' 0
Set-Reg "$ts\WinStations\RDP-Tcp" 'UserAuthentication' 1
Try-Step 'RDP firewall rules' { Enable-NetFirewallRule -Group '@FirewallAPI.dll,-28752' }
Try-Step 'SMBv1 off' { Set-SmbServerConfiguration -EnableSMB1Protocol $false -Force -Confirm:$false }

if ($Profile -eq 'Standard') {
    Try-Step 'allowing ping' { Enable-NetFirewallRule -Name 'FPS-ICMP4-ERQ-In', 'FPS-ICMP6-ERQ-In' -ErrorAction SilentlyContinue }
    # No forced password expiry on the built-in accounts (Server defaults to 42 days).
    Try-Step 'password age' { net accounts /maxpwage:unlimited | Out-Null }
}

# ---- Hardened profile ----
if ($Profile -eq 'Hardened') {
    Log 'hardening: firewall'
    Try-Step 'firewall' {
        Set-NetFirewallProfile -All -Enabled True -DefaultInboundAction Block -DefaultOutboundAction Allow -NotifyOnListen False `
            -LogBlocked True -LogMaxSizeKilobytes 16384
        foreach ($g in '@FirewallAPI.dll,-28502', '@FirewallAPI.dll,-32752', '@FirewallAPI.dll,-33002') {
            # File and Printer Sharing, Network Discovery, Remote Assistance
            Disable-NetFirewallRule -Group $g -ErrorAction SilentlyContinue
        }
    }

    Log 'hardening: Remote Desktop'
    Set-Reg "$ts\WinStations\RDP-Tcp" 'SecurityLayer' 2          # TLS only
    Set-Reg "$ts\WinStations\RDP-Tcp" 'MinEncryptionLevel' 3     # high
    $tsp = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services'
    Set-Reg $tsp 'fDisableCdm' 1              # no drive redirection
    Set-Reg $tsp 'fAllowToGetHelp' 0          # no Remote Assistance
    Set-Reg $tsp 'fPromptForPassword' 1
    Set-Reg $tsp 'fEncryptRPCTraffic' 1
    Set-Reg $tsp 'MaxDisconnectionTime' 28800000  # log off sessions left disconnected for 8 h

    Log 'hardening: accounts and passwords'
    Try-Step 'account policy' { net accounts /minpwlen:12 /maxpwage:unlimited /lockoutthreshold:10 /lockoutwindow:15 /lockoutduration:15 | Out-Null }
    Try-Step 'guest account' { Disable-LocalUser -SID (Get-LocalUser | Where-Object { $_.SID -like '*-501' }).SID }
    $sys = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System'
    Set-Reg $sys 'EnableLUA' 1
    Set-Reg $sys 'ConsentPromptBehaviorAdmin' 2
    Set-Reg $sys 'PromptOnSecureDesktop' 1
    Set-Reg $sys 'FilterAdministratorToken' 1
    Set-Reg $sys 'LocalAccountTokenFilterPolicy' 0   # no remote admin with local accounts over the network
    Set-Reg $sys 'InactivityTimeoutSecs' 900
    Set-Reg $sys 'DontDisplayLastUserName' 1

    Log 'hardening: credentials (LSA, NTLM, WDigest)'
    $lsa = 'HKLM:\SYSTEM\CurrentControlSet\Control\Lsa'
    Set-Reg $lsa 'RunAsPPL' 1
    Set-Reg $lsa 'LmCompatibilityLevel' 5
    Set-Reg $lsa 'NoLMHash' 1
    Set-Reg $lsa 'RestrictAnonymous' 1
    Set-Reg $lsa 'RestrictAnonymousSAM' 1
    Set-Reg $lsa 'EveryoneIncludesAnonymous' 0
    Set-Reg $lsa 'DisableDomainCreds' 1
    Set-Reg "$lsa\MSV1_0" 'NTLMMinClientSec' 537395200
    Set-Reg "$lsa\MSV1_0" 'NTLMMinServerSec' 537395200
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\SecurityProviders\WDigest' 'UseLogonCredential' 0
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' 'CachedLogonsCount' '0' 'String'

    Log 'hardening: SMB'
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Services\LanmanServer\Parameters' 'RequireSecuritySignature' 1
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Services\LanmanServer\Parameters' 'EnableSecuritySignature' 1
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Services\LanmanServer\Parameters' 'RestrictNullSessAccess' 1
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Services\LanmanWorkstation\Parameters' 'RequireSecuritySignature' 1
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Services\LanmanWorkstation\Parameters' 'EnablePlainTextPassword' 0
    Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\LanmanWorkstation' 'AllowInsecureGuestAuth' 0
    Try-Step 'SMBv1 feature' {
        if ($isServer) { Uninstall-WindowsFeature FS-SMB1 -ErrorAction SilentlyContinue | Out-Null }
        else { Disable-WindowsOptionalFeature -Online -FeatureName SMB1Protocol -NoRestart -ErrorAction SilentlyContinue | Out-Null }
    }

    Log 'hardening: name resolution (LLMNR, NetBIOS broadcasts, WPAD)'
    Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows NT\DNSClient' 'EnableMulticast' 0
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Services\NetBT\Parameters' 'NodeType' 2
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Internet Settings\WinHttp' 'DisableWpad' 1

    Log 'hardening: TLS (1.2 and 1.3 only, no RC4 or 3DES)'
    $sc = 'HKLM:\SYSTEM\CurrentControlSet\Control\SecurityProviders\SCHANNEL'
    foreach ($proto in 'SSL 2.0', 'SSL 3.0', 'TLS 1.0', 'TLS 1.1') {
        foreach ($side in 'Server', 'Client') {
            Set-Reg "$sc\Protocols\$proto\$side" 'Enabled' 0
            Set-Reg "$sc\Protocols\$proto\$side" 'DisabledByDefault' 1
        }
    }
    foreach ($side in 'Server', 'Client') {
        Set-Reg "$sc\Protocols\TLS 1.2\$side" 'Enabled' 1
        Set-Reg "$sc\Protocols\TLS 1.2\$side" 'DisabledByDefault' 0
    }
    foreach ($c in 'RC4 128/128', 'RC4 64/128', 'RC4 56/128', 'RC4 40/128', 'Triple DES 168', 'DES 56/56', 'NULL') {
        # The key names contain '/', which PowerShell paths cannot hold.
        $k = [Microsoft.Win32.Registry]::LocalMachine.CreateSubKey("SYSTEM\CurrentControlSet\Control\SecurityProviders\SCHANNEL\Ciphers\$c")
        $k.SetValue('Enabled', 0, 'DWord'); $k.Close()
    }
    foreach ($net in 'SOFTWARE\Microsoft\.NETFramework\v4.0.30319', 'SOFTWARE\WOW6432Node\Microsoft\.NETFramework\v4.0.30319') {
        Set-Reg "HKLM:\$net" 'SchUseStrongCrypto' 1
        Set-Reg "HKLM:\$net" 'SystemDefaultTlsVersions' 1
    }

    Log 'hardening: PowerShell, AutoRun, scripts'
    Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\PowerShell\ScriptBlockLogging' 'EnableScriptBlockLogging' 1
    Try-Step 'PowerShell 2.0 off' {
        if ($isServer) { Uninstall-WindowsFeature PowerShell-V2 -ErrorAction SilentlyContinue | Out-Null }
        else { Disable-WindowsOptionalFeature -Online -FeatureName MicrosoftWindowsPowerShellV2Root -NoRestart -ErrorAction SilentlyContinue | Out-Null }
    }
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\Explorer' 'NoDriveTypeAutoRun' 255
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\Explorer' 'NoAutorun' 1
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows Script Host\Settings' 'Enabled' 0

    Log 'hardening: auditing and event logs'
    # Subcategory GUIDs work on every display language.
    foreach ($g in '{0CCE923F-69AE-11D9-BED3-505054503030}', # Credential Validation
                   '{0CCE9235-69AE-11D9-BED3-505054503030}', # User Account Management
                   '{0CCE9237-69AE-11D9-BED3-505054503030}', # Security Group Management
                   '{0CCE9215-69AE-11D9-BED3-505054503030}', # Logon
                   '{0CCE9216-69AE-11D9-BED3-505054503030}', # Logoff
                   '{0CCE9217-69AE-11D9-BED3-505054503030}', # Account Lockout
                   '{0CCE921B-69AE-11D9-BED3-505054503030}', # Special Logon
                   '{0CCE922B-69AE-11D9-BED3-505054503030}', # Process Creation
                   '{0CCE922F-69AE-11D9-BED3-505054503030}', # Audit Policy Change
                   '{0CCE9211-69AE-11D9-BED3-505054503030}', # Security System Extension
                   '{0CCE9228-69AE-11D9-BED3-505054503030}') { # Sensitive Privilege Use
        Try-Step "audit $g" { auditpol /set /subcategory:$g /success:enable /failure:enable | Out-Null }
    }
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System\Audit' 'ProcessCreationIncludeCmdLine_Enabled' 1
    Set-Reg $lsa 'SCENoApplyLegacyAuditPolicy' 1
    Try-Step 'log sizes' {
        wevtutil sl Security /ms:201326592 | Out-Null
        wevtutil sl System /ms:67108864 | Out-Null
        wevtutil sl Application /ms:67108864 | Out-Null
        wevtutil sl 'Microsoft-Windows-PowerShell/Operational' /ms:67108864 | Out-Null
    }

    Log 'hardening: Microsoft Defender'
    Try-Step 'Defender' {
        Set-MpPreference -DisableRealtimeMonitoring $false -PUAProtection Enabled -EnableNetworkProtection Enabled -DisableBlockAtFirstSeen $false
        # Attack surface reduction rules that do not get in a server's way.
        $asr = @(
            '9e6c4e1f-7d60-472f-ba1a-a39ef669e4b2', # credential stealing from LSASS
            '56a863a9-875e-4185-98a7-b882c64b5ce5', # abuse of vulnerable signed drivers
            'e6db77e5-3df2-4cf1-b95a-636979351e5b', # persistence through WMI event subscriptions
            'be9ba2d9-53ea-4cdc-84e5-9b1eeee46550', # executable content from email
            'd4f940ab-401b-4efc-aadc-ad5f3c50688a', # Office apps creating child processes
            '3b576869-a4ec-4529-8536-b80a7769e899', # Office apps creating executable content
            'd3e037e1-3eb8-44c8-a917-57927947596d'  # JavaScript/VBScript launching downloads
        )
        Add-MpPreference -AttackSurfaceReductionRules_Ids $asr -AttackSurfaceReductionRules_Actions ($asr | ForEach-Object { 'Enabled' })
    }

    Log 'hardening: services'
    Disable-Svc @('RemoteRegistry', 'Spooler', 'SSDPSRV', 'upnphost', 'lfsvc', 'MapsBroker', 'RetailDemo', 'WMPNetworkSvc',
        'XblAuthManager', 'XblGameSave', 'XboxNetApiSvc', 'XboxGipSvc', 'Fax', 'SharedAccess', 'WerSvc')

    Log 'hardening: telemetry and consumer features'
    Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\DataCollection' 'AllowTelemetry' 0   # the lowest each edition allows
    Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\AdvertisingInfo' 'DisabledByGroupPolicy' 1
    Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\System' 'EnableActivityFeed' 0
    Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\Windows Search' 'AllowCortana' 0
}

# ---- Cloudbase-Init (cloud-init for Windows) ----
if (-not (Test-Path "$CloudbaseDir\Python\Scripts\cloudbase-init.exe")) {
    Log "downloading Cloudbase-Init from $CloudbaseMsi"
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $msi = Join-Path $env:TEMP 'CloudbaseInitSetup.msi'
    for ($i = 1; ; $i++) {
        try { Invoke-WebRequest -Uri $CloudbaseMsi -OutFile $msi -UseBasicParsing; break }
        catch {
            if ($i -ge 10) { throw "cannot download Cloudbase-Init (does the VM have internet access?): $($_.Exception.Message)" }
            Log "download failed, retrying: $($_.Exception.Message)"; Start-Sleep -Seconds 15
        }
    }
    $sig = Get-AuthenticodeSignature $msi
    if ($sig.Status -ne 'Valid' -or $sig.SignerCertificate.Subject -notmatch 'Cloudbase Solutions') {
        throw "Cloudbase-Init installer signature is not valid ($($sig.Status), $($sig.SignerCertificate.Subject))"
    }
    Log "signature OK: $($sig.SignerCertificate.Subject)"
    $code = Invoke-Installer msiexec.exe @('/i', "`"$msi`"", '/qn', '/norestart', '/l*v', 'C:\Windows\Temp\cloudbase-init-install.log',
        'LOGGINGSERIALPORTNAME=""', 'USERNAME=Administrator', 'RUN_SERVICE_AS_LOCAL_SYSTEM=1') 20
    if ($code -ne 0 -and $code -ne 3010) { throw "Cloudbase-Init install failed with exit code $code" }
    Remove-Item $msi -Force -ErrorAction SilentlyContinue
}
Log 'configuring Cloudbase-Init for Proxmox (config drive)'
$common = @"
username=Administrator
groups=Administrators
inject_user_password=true
first_logon_behaviour=no
rename_admin_user=false
netbios_host_name_compatibility=true
config_drive_raw_hhd=true
config_drive_cdrom=true
config_drive_vfat=true
bsdtar_path=$CloudbaseDir\bin\bsdtar.exe
mtools_path=$CloudbaseDir\bin\
verbose=true
debug=false
log_dir=$CloudbaseDir\log\
default_log_levels=comtypes=INFO,suds=INFO,iso8601=WARN,requests=WARN
local_scripts_path=$CloudbaseDir\LocalScripts\
metadata_services=cloudbaseinit.metadata.services.configdrive.ConfigDriveService
check_latest_version=false
stop_service_on_exit=false
"@
$conf = "[DEFAULT]`r`n$common`r`nlog_file=cloudbase-init.log`r`nallow_reboot=true`r`n" +
    'plugins=cloudbaseinit.plugins.common.mtu.MTUPlugin,cloudbaseinit.plugins.common.sethostname.SetHostNamePlugin,' +
    'cloudbaseinit.plugins.common.networkconfig.NetworkConfigPlugin,cloudbaseinit.plugins.windows.extendvolumes.ExtendVolumesPlugin,' +
    'cloudbaseinit.plugins.windows.createuser.CreateUserPlugin,cloudbaseinit.plugins.common.setuserpassword.SetUserPasswordPlugin,' +
    "cloudbaseinit.plugins.common.localscripts.LocalScriptsPlugin`r`n"
$unattend = "[DEFAULT]`r`n$common`r`nlog_file=cloudbase-init-unattend.log`r`nallow_reboot=false`r`n" +
    'plugins=cloudbaseinit.plugins.common.mtu.MTUPlugin,cloudbaseinit.plugins.common.sethostname.SetHostNamePlugin,' +
    "cloudbaseinit.plugins.windows.extendvolumes.ExtendVolumesPlugin`r`n"
Set-Content -Path "$CloudbaseDir\conf\cloudbase-init.conf" -Value $conf -Encoding ASCII
Set-Content -Path "$CloudbaseDir\conf\cloudbase-init-unattend.conf" -Value $unattend -Encoding ASCII
New-Item -ItemType Directory -Path "$CloudbaseDir\LocalScripts" -Force | Out-Null
# Sysprep disables the built-in Administrator on Windows 10/11, and
# Cloudbase-Init sets its password without enabling it. This runs once on
# a deployed VM's first boot, after the password plugin.
Set-Content -Path "$CloudbaseDir\LocalScripts\servermgr-enable-admin.ps1" -Encoding ASCII -Value @'
Get-LocalUser | Where-Object { $_.SID -like '*-500' } | Enable-LocalUser
'@

# Administrator is a deployed VM's only account: a lockout after wrong
# passwords would shut its owner out, so it is exempt (Windows 11 locks
# it by default). Other accounts keep the lockout policy.
Try-Step 'Administrator lockout exemption' {
    $inf = Join-Path $env:TEMP 'servermgr-lockout.inf'
    $sdb = Join-Path $env:TEMP 'servermgr-lockout.sdb'
    Set-Content -Path $inf -Encoding Unicode -Value "[Unicode]`r`nUnicode=yes`r`n[System Access]`r`nAllowAdministratorLockout = 0`r`n[Version]`r`nsignature=`"`$CHICAGO`$`"`r`nRevision=1"
    secedit /configure /db $sdb /cfg $inf /areas SECURITYPOLICY /quiet | Out-Null
    Remove-Item $inf, $sdb -Force -ErrorAction SilentlyContinue
}

# ---- Updates after deployment ----
Remove-ItemProperty -Path $wu -Name 'NoAutoUpdate' -ErrorAction SilentlyContinue
if ($Profile -eq 'Hardened') {
    Set-Reg $wu 'NoAutoUpdate' 0
    Set-Reg $wu 'AUOptions' 4            # download and install automatically
    Set-Reg $wu 'ScheduledInstallDay' 0
    Set-Reg $wu 'ScheduledInstallTime' 3
    Set-Reg $wu 'NoAutoRebootWithLoggedOnUsers' 1
}

# ---- Applications picked for the template ----
# A build CD carries the admin's own product key when one was given: Setup
# installed with the image's generic key, so an edition mismatch could not
# stop it. The key is installed now; each VM activates it once online.
$keyFile = Join-Path $PSScriptRoot 'servermgr-key.txt'
if ($PSScriptRoot -and (Test-Path $keyFile)) {
    Try-Step 'product key' {
        $key = (Get-Content -Raw -Path $keyFile).Trim()
        $sls = Get-CimInstance SoftwareLicensingService
        Invoke-CimMethod -InputObject $sls -MethodName InstallProductKey -Arguments @{ ProductKey = $key } | Out-Null
        Invoke-CimMethod -InputObject $sls -MethodName RefreshLicenseStatus | Out-Null
    }
}

# A build CD carries servermgr-apps.ps1 and the list next to this script.
$appsList = Join-Path $PSScriptRoot 'servermgr-apps.json'
$appsScript = Join-Path $PSScriptRoot 'servermgr-apps.ps1'
if ($PSScriptRoot -and (Test-Path $appsList) -and (Test-Path $appsScript)) {
    Log 'installing the applications picked for this template'
    Try-Step 'applications' { & $appsScript -Json $appsList -LogFile $LogFile }
}

# ---- Clean-up so the template is small and Sysprep succeeds ----
Log 'removing per-user Store app updates that would stop Sysprep'
Try-Step 'Store apps' {
    $provisioned = (Get-AppxProvisionedPackage -Online).DisplayName
    Get-AppxPackage -AllUsers | Where-Object { -not $_.NonRemovable -and $_.SignatureKind -eq 'Store' -and $provisioned -notcontains $_.Name } |
        ForEach-Object { Remove-AppxPackage -Package $_.PackageFullName -AllUsers -ErrorAction SilentlyContinue }
}
Log 'cleaning up the component store (this can take a while)'
Try-Step 'DISM' { Dism.exe /Online /Cleanup-Image /StartComponentCleanup /Quiet | Out-Null }
Try-Step 'temp files' {
    Remove-Item -Path "$env:TEMP\*", 'C:\Windows\Temp\*' -Recurse -Force -ErrorAction SilentlyContinue -Exclude 'servermgr-build.log', 'cloudbase-init-install.log'
    Remove-Item -Path 'C:\Windows\SoftwareDistribution\Download\*' -Recurse -Force -ErrorAction SilentlyContinue
}
Try-Step 'TRIM' { Optimize-Volume -DriveLetter C -ReTrim }
if ($Build) {
    # The build's auto-logon and its password must not survive into the template.
    $wl = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon'
    Set-Reg $wl 'AutoAdminLogon' '0' 'String'
    Remove-ItemProperty -Path $wl -Name 'DefaultPassword', 'AutoLogonCount' -ErrorAction SilentlyContinue
}

if ($NoSysprep) {
    Log 'done; Sysprep skipped (-NoSysprep). Run this again without it when ready.'
    return
}
if (-not $Build) {
    $answer = Read-Host 'Ready to run Sysprep, which generalizes Windows and shuts the VM down. Continue? (y/N)'
    if ($answer -notmatch '^[yY]') { Log 'stopped before Sysprep'; return }
}
Log 'SERVERMGR-SYSPREP running Sysprep; the VM shuts down when it is done'
Try-Step 'events' { $ErrorActionPreference = 'SilentlyContinue'; wevtutil el | ForEach-Object { wevtutil cl "$_" 2>$null } }
# Give servermgr a moment to read the log before the VM goes away.
if ($Build) { Start-Sleep -Seconds 30 }
& "$env:SystemRoot\System32\Sysprep\sysprep.exe" /generalize /oobe /shutdown /quiet "/unattend:$CloudbaseDir\conf\Unattend.xml"
