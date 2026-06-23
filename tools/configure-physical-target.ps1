<#
.SYNOPSIS
Configures one physical PXE/BMC/SSH target through the Baremetal Platform API.

.DESCRIPTION
This operator helper prepares a real hardware target for strict validation:
  - creates or updates the server inventory row
  - upserts a physical Redfish/IPMI BMC endpoint
  - upserts real SSH access
  - optionally invokes tools/physical-validation.ps1 for strict evidence checks

Secrets are read from environment variables instead of command-line arguments:
  - BAREMETAL_ADMIN_PASSWORD for the platform admin login
  - BAREMETAL_BMC_PASSWORD for the BMC password
  - BAREMETAL_SSH_SECRET for the SSH password or private key
#>

[CmdletBinding()]
param(
    [string]$BaseUrl = "http://127.0.0.1:8080",

    [Parameter(Mandatory = $true)]
    [string]$Email,

    [string]$PasswordEnvVar = "BAREMETAL_ADMIN_PASSWORD",

    [uint32]$ServerId = 0,

    [string]$AssetNo = "",

    [string]$Hostname = "",

    [string]$PXEMac = "",

    [string]$Architecture = "x86_64",

    [string]$Status = "ready",

    [string]$PrimaryIP = "",

    [string]$TenantID = "",

    [string]$Owner = "",

    [string]$Location = "",

    [string]$Rack = "",

    [string]$RackUnit = "",

    [ValidateSet("redfish", "ipmi")]
    [string]$BMCType = "redfish",

    [string]$BMCProtocol = "",

    [string]$BMCEndpoint = "",

    [string]$BMCUsername = "",

    [string]$BMCPasswordEnvVar = "BAREMETAL_BMC_PASSWORD",

    [switch]$SkipBMC,

    [string]$SSHHost = "",

    [int]$SSHPort = 22,

    [string]$SSHUsername = "",

    [ValidateSet("password", "private_key")]
    [string]$SSHAuthType = "password",

    [string]$SSHSecretEnvVar = "BAREMETAL_SSH_SECRET",

    [switch]$SkipSSH,

    [switch]$ValidateNow,

    [ValidateSet(0, 7, 9, 11)]
    [int]$PXEArch = 9,

    [string]$SSHProbeCommand = "",

    [string]$OutDir = ".\lab-validation-output",

    [switch]$AllowDegradedReadyz,

    [switch]$RecordEvidence,

    [switch]$RecordFullEvidence
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Join-ApiUrl {
    param(
        [Parameter(Mandatory = $true)][string]$Root,
        [Parameter(Mandatory = $true)][string]$Path
    )
    return $Root.TrimEnd("/") + "/" + $Path.TrimStart("/")
}

function Invoke-JsonRequest {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Url,
        [object]$Body = $null,
        [hashtable]$Headers = @{}
    )

    $request = @{
        Method      = $Method
        Uri         = $Url
        Headers     = $Headers
        ErrorAction = "Stop"
    }
    if ($null -ne $Body) {
        $request.ContentType = "application/json"
        $request.Body = ($Body | ConvertTo-Json -Depth 32)
    }
    return Invoke-RestMethod @request
}

function Read-PlainPassword {
    param([Parameter(Mandatory = $true)][string]$Prompt)
    $secure = Read-Host -Prompt $Prompt -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    }
    finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
}

function Read-RequiredSecret {
    param(
        [Parameter(Mandatory = $true)][string]$EnvVar,
        [Parameter(Mandatory = $true)][string]$Prompt
    )
    if (-not [string]::IsNullOrWhiteSpace($EnvVar)) {
        $fromEnv = [Environment]::GetEnvironmentVariable($EnvVar)
        if (-not [string]::IsNullOrWhiteSpace($fromEnv)) {
            Write-Host "Using secret from environment variable $EnvVar"
            return $fromEnv
        }
    }
    return Read-PlainPassword -Prompt $Prompt
}

function Add-IfPresent {
    param(
        [Parameter(Mandatory = $true)][hashtable]$Target,
        [Parameter(Mandatory = $true)][string]$Key,
        [object]$Value
    )
    if ($null -eq $Value) {
        return
    }
    if ($Value -is [string] -and [string]::IsNullOrWhiteSpace($Value)) {
        return
    }
    $Target[$Key] = $Value
}

function Get-ObjectProperty {
    param(
        [object]$Value,
        [Parameter(Mandatory = $true)][string]$Name
    )
    if ($null -eq $Value) {
        return $null
    }
    $prop = $Value.PSObject.Properties[$Name]
    if ($null -eq $prop) {
        return $null
    }
    return $prop.Value
}

function Normalize-MacText {
    param([string]$Value)
    $hex = ($Value -replace "[^0-9A-Fa-f]", "").ToLowerInvariant()
    if ($hex.Length -ne 12) {
        return ""
    }
    return (($hex -split "(.{2})" | Where-Object { $_ }) -join ":")
}

function Find-Server {
    param(
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [string]$Asset,
        [string]$Mac
    )
    $keywords = @()
    if (-not [string]::IsNullOrWhiteSpace($Asset)) {
        $keywords += $Asset
    }
    if (-not [string]::IsNullOrWhiteSpace($Mac)) {
        $keywords += $Mac
    }
    foreach ($keyword in $keywords) {
        $encoded = [uri]::EscapeDataString($keyword)
        $result = Invoke-JsonRequest -Method "GET" -Url (Join-ApiUrl $apiRoot "servers?keyword=$encoded&page=1&page_size=100") -Headers $Headers
        $items = @(Get-ObjectProperty $result "items")
        if ($items.Count -eq 0 -and $result -is [array]) {
            $items = @($result)
        }
        foreach ($item in $items) {
            $itemAsset = [string](Get-ObjectProperty $item "asset_no")
            $itemMac = Normalize-MacText ([string](Get-ObjectProperty $item "primary_mac"))
            if ((-not [string]::IsNullOrWhiteSpace($Asset) -and $itemAsset -eq $Asset) -or
                (-not [string]::IsNullOrWhiteSpace($Mac) -and $itemMac -eq (Normalize-MacText $Mac))) {
                return $item
            }
        }
    }
    return $null
}

function Resolve-Server {
    param([Parameter(Mandatory = $true)][hashtable]$Headers)
    if ($ServerId -gt 0) {
        return Invoke-JsonRequest -Method "GET" -Url (Join-ApiUrl $apiRoot "servers/$ServerId") -Headers $Headers
    }

    $existing = Find-Server -Headers $Headers -Asset $AssetNo -Mac $PXEMac
    if ($null -ne $existing) {
        Write-Host "Using existing server id=$($existing.id)"
        return $existing
    }

    if ([string]::IsNullOrWhiteSpace($AssetNo) -and [string]::IsNullOrWhiteSpace($Hostname) -and [string]::IsNullOrWhiteSpace($PXEMac)) {
        throw "Provide -ServerId, or at least one of -AssetNo, -Hostname, or -PXEMac to create/find a physical target."
    }

    $body = @{}
    Add-IfPresent -Target $body -Key "asset_no" -Value $AssetNo
    Add-IfPresent -Target $body -Key "hostname" -Value $Hostname
    Add-IfPresent -Target $body -Key "primary_mac" -Value $PXEMac
    Add-IfPresent -Target $body -Key "architecture" -Value $Architecture
    Add-IfPresent -Target $body -Key "status" -Value $Status
    Add-IfPresent -Target $body -Key "primary_ip" -Value $PrimaryIP
    Add-IfPresent -Target $body -Key "tenant_id" -Value $TenantID
    Add-IfPresent -Target $body -Key "owner" -Value $Owner
    Add-IfPresent -Target $body -Key "location" -Value $Location
    Add-IfPresent -Target $body -Key "rack" -Value $Rack
    Add-IfPresent -Target $body -Key "rack_unit" -Value $RackUnit
    Write-Host "Creating physical target inventory row"
    return Invoke-JsonRequest -Method "POST" -Url (Join-ApiUrl $apiRoot "servers") -Headers $Headers -Body $body
}

function Configure-BMC {
    param(
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [Parameter(Mandatory = $true)][uint32]$TargetServerId
    )
    if ($SkipBMC) {
        Write-Host "Skipping BMC configuration"
        return
    }
    if ([string]::IsNullOrWhiteSpace($BMCEndpoint) -or [string]::IsNullOrWhiteSpace($BMCUsername)) {
        throw "Provide -BMCEndpoint and -BMCUsername, or use -SkipBMC."
    }
    $secret = Read-RequiredSecret -EnvVar $BMCPasswordEnvVar -Prompt "BMC password"
    try {
        $body = @{
            type     = $BMCType
            endpoint = $BMCEndpoint
            username = $BMCUsername
            password = $secret
        }
        Add-IfPresent -Target $body -Key "protocol" -Value $BMCProtocol
        Write-Host "Configuring BMC endpoint for server_id=$TargetServerId"
        Invoke-JsonRequest -Method "POST" -Url (Join-ApiUrl $apiRoot "servers/$TargetServerId/bmc") -Headers $Headers -Body $body | Out-Null
    }
    finally {
        $secret = $null
    }
}

function Configure-SSH {
    param(
        [Parameter(Mandatory = $true)][hashtable]$Headers,
        [Parameter(Mandatory = $true)][uint32]$TargetServerId
    )
    if ($SkipSSH) {
        Write-Host "Skipping SSH configuration"
        return
    }
    if ([string]::IsNullOrWhiteSpace($SSHHost) -or [string]::IsNullOrWhiteSpace($SSHUsername)) {
        throw "Provide -SSHHost and -SSHUsername, or use -SkipSSH."
    }
    if ($SSHPort -lt 1 -or $SSHPort -gt 65535) {
        throw "-SSHPort must be between 1 and 65535."
    }
    $secret = Read-RequiredSecret -EnvVar $SSHSecretEnvVar -Prompt "SSH secret"
    try {
        $body = @{
            host      = $SSHHost
            port      = $SSHPort
            username  = $SSHUsername
            auth_type = $SSHAuthType
            secret    = $secret
        }
        Write-Host "Configuring SSH access for server_id=$TargetServerId"
        Invoke-JsonRequest -Method "POST" -Url (Join-ApiUrl $apiRoot "servers/$TargetServerId/ssh") -Headers ($Headers + @{ "X-Confirm-Action" = "ssh.upsert" }) -Body $body | Out-Null
    }
    finally {
        $secret = $null
    }
}

function Invoke-PhysicalValidation {
    param([Parameter(Mandatory = $true)][uint32]$TargetServerId)
    if ([string]::IsNullOrWhiteSpace($PXEMac)) {
        throw "-PXEMac is required when -ValidateNow is set."
    }
    $script = Join-Path $PSScriptRoot "physical-validation.ps1"
    if (-not (Test-Path -LiteralPath $script)) {
        throw "physical-validation.ps1 not found at $script"
    }
    $args = @(
        "-BaseUrl", $root,
        "-Email", $Email,
        "-PasswordEnvVar", $PasswordEnvVar,
        "-ServerId", $TargetServerId,
        "-PXEMac", $PXEMac,
        "-PXEArch", $PXEArch,
        "-OutDir", $OutDir
    )
    if (-not [string]::IsNullOrWhiteSpace($SSHProbeCommand)) {
        $args += @("-SSHProbeCommand", $SSHProbeCommand)
    }
    if ($AllowDegradedReadyz) {
        $args += "-AllowDegradedReadyz"
    }
    if ($RecordEvidence) {
        $args += "-RecordEvidence"
    }
    if ($RecordFullEvidence) {
        $args += "-RecordFullEvidence"
    }
    Write-Host "Running strict physical validation for server_id=$TargetServerId"
    & $script @args
}

$root = $BaseUrl.TrimEnd("/")
$apiRoot = Join-ApiUrl $root "api/v1"

Write-Host "Checking readiness at $root/readyz"
$readyz = Invoke-JsonRequest -Method "GET" -Url (Join-ApiUrl $root "readyz")
if ($readyz.status -ne "ok") {
    $messages = @($readyz.checks | Where-Object { $_.status -ne "ok" } | ForEach-Object { "$($_.name): $($_.status) - $($_.message)" })
    Write-Host "Readiness is $($readyz.status): $($messages -join '; ')"
}

Write-Host "Logging in as $Email"
$adminPassword = Read-RequiredSecret -EnvVar $PasswordEnvVar -Prompt "Admin password"
try {
    $login = Invoke-JsonRequest -Method "POST" -Url (Join-ApiUrl $apiRoot "auth/login") -Body @{ email = $Email; password = $adminPassword }
}
finally {
    $adminPassword = $null
}
if ([string]::IsNullOrWhiteSpace($login.token)) {
    throw "Login response did not include a token."
}

$headers = @{
    Authorization  = "Bearer $($login.token)"
    "X-Request-ID" = "configure-physical-target-$(Get-Date -Format "yyyyMMdd-HHmmss")"
}

$server = Resolve-Server -Headers $headers
$targetServerId = [uint32](Get-ObjectProperty $server "id")
if ($targetServerId -le 0) {
    throw "Could not resolve configured server id."
}
Write-Host "Physical target server_id=$targetServerId asset_no=$([string](Get-ObjectProperty $server "asset_no")) hostname=$([string](Get-ObjectProperty $server "hostname"))"

Configure-BMC -Headers $headers -TargetServerId $targetServerId
Configure-SSH -Headers $headers -TargetServerId $targetServerId

if ($ValidateNow) {
    Invoke-PhysicalValidation -TargetServerId $targetServerId
} else {
    Write-Host "Configuration complete. Re-run with -ValidateNow or run tools/physical-validation.ps1 when the PXE client has booted."
}
