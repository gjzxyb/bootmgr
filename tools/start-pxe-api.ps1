<#
.SYNOPSIS
Starts the Baremetal Platform API with real PXE DHCP/TFTP listeners enabled.

.DESCRIPTION
This helper is intended for a lab/deployment VLAN only. The default values are
set for the current Windows host deployment NIC:
  - interface: 以太网
  - IP:        192.168.1.88/24

It runs ProxyDHCP by default, so the platform only answers PXE clients and does
not lease normal workstation addresses. Use builtin DHCP only on an isolated
deployment network with an explicit lease range.
#>

[CmdletBinding()]
param(
    [string]$ApiExe = "",
    [string]$WorkingDirectory = "",
    [string]$DeploymentIP = "192.168.1.88",
    [string]$BindInterface = "以太网",
    [ValidateSet("proxy", "builtin", "external")]
    [string]$BootServiceMode = "proxy",
    [string]$DhcpListenAddr = "",
    [string]$TftpListenAddr = "",
    [string]$DhcpServerIP = "",
    [string]$DhcpLeaseStart = "",
    [string]$DhcpLeaseEnd = "",
    [string]$TftpRoot = "",
    [string]$BootBaseUrl = "",
    [string]$HttpAddr = ":8080",
    [string]$ImageStorageDir = "",
    [switch]$StopExisting
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($WorkingDirectory)) {
    $WorkingDirectory = Join-Path $repoRoot "backend"
}
$WorkingDirectory = (Resolve-Path -LiteralPath $WorkingDirectory).Path

if ([string]::IsNullOrWhiteSpace($ApiExe)) {
    $ApiExe = Join-Path $WorkingDirectory "api.exe"
}
$ApiExe = (Resolve-Path -LiteralPath $ApiExe).Path

if ([string]::IsNullOrWhiteSpace($DhcpListenAddr)) {
    $DhcpListenAddr = "${DeploymentIP}:67"
}
if ([string]::IsNullOrWhiteSpace($TftpListenAddr)) {
    $TftpListenAddr = "${DeploymentIP}:69"
}
if ([string]::IsNullOrWhiteSpace($DhcpServerIP)) {
    $DhcpServerIP = $DeploymentIP
}
if ([string]::IsNullOrWhiteSpace($BootBaseUrl)) {
    $BootBaseUrl = "http://${DeploymentIP}:8080"
}
if ([string]::IsNullOrWhiteSpace($TftpRoot)) {
    $TftpRoot = Join-Path $WorkingDirectory "data\tftp"
}
if ([string]::IsNullOrWhiteSpace($ImageStorageDir)) {
    $ImageStorageDir = Join-Path $WorkingDirectory "data\images"
}

New-Item -ItemType Directory -Force -Path $TftpRoot | Out-Null
New-Item -ItemType Directory -Force -Path $ImageStorageDir | Out-Null

if ($StopExisting) {
    $existing = Get-CimInstance Win32_Process -Filter "name = 'api.exe'" |
        Where-Object { $_.ExecutablePath -eq $ApiExe }
    foreach ($proc in $existing) {
        Write-Host "Stopping existing api.exe PID $($proc.ProcessId)"
        Stop-Process -Id $proc.ProcessId -Force
    }
}

$corsOrigins = @(
    "http://localhost:5173",
    "http://127.0.0.1:5173",
    "http://${DeploymentIP}:5173",
    "http://localhost:8081",
    "http://127.0.0.1:8081"
) -join ","

$envVars = @{
    APP_ENV = "development"
    HTTP_ADDR = $HttpAddr
    CORS_ALLOWED_ORIGINS = $corsOrigins
    DB_DRIVER = "sqlite"
    DATABASE_URL = "file:baremetal.db?cache=shared"
    BOOT_SERVICES_ENABLED = "true"
    BOOT_SERVICE_MODE = $BootServiceMode
    BOOT_BIND_INTERFACE = $BindInterface
    BOOT_DHCP_LISTEN_ADDR = $DhcpListenAddr
    BOOT_DHCP_SERVER_IP = $DhcpServerIP
    BOOT_DHCP_LEASE_START = $DhcpLeaseStart
    BOOT_DHCP_LEASE_END = $DhcpLeaseEnd
    BOOT_TFTP_LISTEN_ADDR = $TftpListenAddr
    BOOT_TFTP_ROOT = $TftpRoot
    BOOT_TFTP_BOOTFILE_UEFI = "ipxe.efi"
    BOOT_TFTP_BOOTFILE_BIOS = "undionly.kpxe"
    BOOT_BASE_URL = $BootBaseUrl
    METADATA_REQUIRE_DEPLOYMENT_NETWORK = "true"
    IMAGE_STORAGE_DIR = $ImageStorageDir
    IMAGE_UPLOAD_MAX_MB = "20480"
    ENABLE_DEMO_SEEDER = "true"
    BMC_ADAPTER = "simulated"
    COLLECTOR_MODE = "simulated"
    SSH_OPERATIONS_MODE = "simulated"
    SSH_HOST_KEY_POLICY = "insecure_ignore"
}

foreach ($entry in $envVars.GetEnumerator()) {
    [Environment]::SetEnvironmentVariable($entry.Key, [string]$entry.Value, "Process")
}

$stdoutLog = Join-Path $WorkingDirectory "api-pxe.out.log"
$stderrLog = Join-Path $WorkingDirectory "api-pxe.err.log"

Write-Host "Starting API with PXE services enabled"
Write-Host "  API:       $ApiExe"
Write-Host "  Workdir:   $WorkingDirectory"
Write-Host "  DHCP:      $DhcpListenAddr mode=$BootServiceMode server=$DhcpServerIP"
Write-Host "  TFTP:      $TftpListenAddr root=$TftpRoot"
Write-Host "  Base URL:  $BootBaseUrl"
Write-Host "  Logs:      $stdoutLog / $stderrLog"

$process = Start-Process `
    -FilePath $ApiExe `
    -WorkingDirectory $WorkingDirectory `
    -WindowStyle Hidden `
    -RedirectStandardOutput $stdoutLog `
    -RedirectStandardError $stderrLog `
    -PassThru

Start-Sleep -Seconds 2
if ($process.HasExited) {
    $stderrTail = ""
    if (Test-Path -LiteralPath $stderrLog) {
        $stderrTail = (Get-Content -LiteralPath $stderrLog -Tail 40) -join [Environment]::NewLine
    }
    throw "api.exe exited early with code $($process.ExitCode). Recent stderr:$([Environment]::NewLine)$stderrTail"
}

Write-Host "api.exe started with PID $($process.Id)"
