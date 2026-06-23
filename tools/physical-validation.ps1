<#
.SYNOPSIS
Runs a strict physical lab validation pass through the Baremetal Platform API.

.DESCRIPTION
This script is an operator helper for real PXE/DHCP/TFTP, physical Redfish/IPMI,
and real SSH host validation. It does not enable DHCP/TFTP listeners and does
not perform power actions. It only calls the existing API:
  - GET /readyz
  - POST /api/v1/auth/login
  - POST /api/v1/system/lab-validation/run
  - GET /api/v1/system/lab-validation/runs/{id}/evidence-bundle
  - POST /api/v1/system/lab-validation/evidence, only when -RecordEvidence or
    -RecordFullEvidence is set

The output JSON files can be attached to a change record or used when recording
physical evidence in the system management page.

For unattended validation, set BAREMETAL_ADMIN_PASSWORD or pass a different
-PasswordEnvVar name. The password is read from the environment instead of a
command-line argument so it is less likely to appear in shell history.
#>

[CmdletBinding()]
param(
    [string]$BaseUrl = "http://127.0.0.1:8080",

    [Parameter(Mandatory = $true)]
    [string]$Email,

    [string]$PasswordEnvVar = "BAREMETAL_ADMIN_PASSWORD",

    [string[]]$ServerId = @(),

    [string[]]$PXEMac = @(),

    [ValidateSet(0, 7, 9, 11)]
    [int]$PXEArch = 9,

    [string]$SSHProbeCommand = "",

    [string]$OutDir = ".\lab-validation-output",

    [switch]$AllowDegradedReadyz,

    [switch]$RecordEvidence,

    [switch]$RecordFullEvidence,

    [string]$EvidenceSummary = "Full-chain physical validation evidence recorded by tools/physical-validation.ps1",

    [string]$EvidenceDetails = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Expand-ArgumentList {
    param([object[]]$Values)

    $expanded = @()
    foreach ($value in @($Values)) {
        if ($null -eq $value) {
            continue
        }
        foreach ($part in ([string]$value -split ",")) {
            $clean = $part.Trim()
            $clean = $clean.Trim("'").Trim('"').Trim()
            if (-not [string]::IsNullOrWhiteSpace($clean)) {
                $expanded += $clean
            }
        }
    }
    return $expanded
}

function Normalize-ServerIdArgs {
    param([object[]]$Values)

    $ids = @()
    foreach ($raw in (Expand-ArgumentList $Values)) {
        $id = 0
        if (-not [uint32]::TryParse([string]$raw, [ref]$id) -or $id -eq 0) {
            throw "Invalid -ServerId value `"$raw`"."
        }
        $ids += [uint32]$id
    }
    return [uint32[]]$ids
}

function Normalize-PXEMacArgs {
    param([object[]]$Values)

    return [string[]](Expand-ArgumentList $Values)
}

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

function Save-JsonFile {
    param(
        [Parameter(Mandatory = $true)][object]$Value,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $Value | ConvertTo-Json -Depth 64 | Set-Content -LiteralPath $Path -Encoding UTF8
}

function Read-PlainPassword {
    $secure = Read-Host -Prompt "Admin password" -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    }
    finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
}

function Read-AdminPassword {
    if (-not [string]::IsNullOrWhiteSpace($PasswordEnvVar)) {
        $fromEnv = [Environment]::GetEnvironmentVariable($PasswordEnvVar)
        if (-not [string]::IsNullOrWhiteSpace($fromEnv)) {
            Write-Host "Using admin password from environment variable $PasswordEnvVar"
            return $fromEnv
        }
    }
    return Read-PlainPassword
}

function Assert-StrictInputs {
    if ($ServerId.Count -eq 0) {
        throw "At least one -ServerId is required for strict physical BMC/SSH validation."
    }
    if ($PXEMac.Count -eq 0) {
        throw "At least one -PXEMac is required for strict physical PXE validation."
    }
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

function Test-ServerRequested {
    param([uint32]$Candidate)
    foreach ($id in $ServerId) {
        if ([uint32]$id -eq $Candidate) {
            return $true
        }
    }
    return $false
}

function Normalize-MacText {
    param([object]$Value)
    if ($null -eq $Value) {
        return ""
    }
    $hex = ([string]$Value).Trim().ToLowerInvariant() -replace "[^0-9a-f]", ""
    if ($hex.Length -ne 12) {
        return ""
    }
    $parts = @()
    for ($i = 0; $i -lt 12; $i += 2) {
        $parts += $hex.Substring($i, 2)
    }
    return ($parts -join ":")
}

function Test-PXEMacRequested {
    param([object]$Candidate)
    $normalized = Normalize-MacText $Candidate
    if ([string]::IsNullOrWhiteSpace($normalized)) {
        return $false
    }
    foreach ($mac in $PXEMac) {
        if ((Normalize-MacText $mac) -eq $normalized) {
            return $true
        }
    }
    return $false
}

function Get-ChecklistItem {
    param(
        [object]$Bundle,
        [uint32]$CandidateServerId,
        [Parameter(Mandatory = $true)][string]$Step
    )
    $items = @(Get-ObjectProperty $Bundle "operator_checklist")
    foreach ($item in $items) {
        $server = Get-ObjectProperty $item "server_id"
        $itemStep = Get-ObjectProperty $item "step"
        if ($null -eq $server -or $null -eq $itemStep) {
            continue
        }
        if ([uint32]$server -eq $CandidateServerId -and [string]$itemStep -eq $Step) {
            return $item
        }
    }
    return $null
}

function Test-EvidenceCandidateRequested {
    param(
        [object]$Bundle,
        [object]$Candidate
    )
    $kind = [string](Get-ObjectProperty $Candidate "Kind")
    $serverID = Get-ObjectProperty $Candidate "ServerID"
    $bootEventID = Get-ObjectProperty $Candidate "BootEventID"

    if ($kind -eq "bmc" -or $kind -eq "ssh") {
        return $null -ne $serverID -and [uint32]$serverID -gt 0 -and (Test-ServerRequested -Candidate ([uint32]$serverID))
    }

    if ($kind -eq "pxe") {
        if ($null -ne $serverID -and [uint32]$serverID -gt 0 -and -not (Test-ServerRequested -Candidate ([uint32]$serverID))) {
            return $false
        }
        if (Test-PXEMacRequested (Get-ObjectProperty $Candidate "Subject")) {
            return $true
        }
        if ($null -ne $bootEventID -and [uint32]$bootEventID -gt 0) {
            $event = Get-BundleBootEvent -Bundle $Bundle -BootEventID ([uint32]$bootEventID)
            return $null -ne $event -and (Test-PXEMacRequested (Get-ObjectProperty $event "mac"))
        }
        return $false
    }

    if ($kind -eq "full") {
        if ($null -eq $serverID -or [uint32]$serverID -eq 0 -or -not (Test-ServerRequested -Candidate ([uint32]$serverID))) {
            return $false
        }
        if ($null -eq $bootEventID -or [uint32]$bootEventID -eq 0) {
            return $false
        }
        $event = Get-BundleBootEvent -Bundle $Bundle -BootEventID ([uint32]$bootEventID)
        return $null -ne $event -and (Test-PXEMacRequested (Get-ObjectProperty $event "mac"))
    }

    return $false
}

function Test-ChecklistOK {
    param(
        [object]$Bundle,
        [uint32]$CandidateServerId,
        [Parameter(Mandatory = $true)][string]$Step
    )
    $item = Get-ChecklistItem -Bundle $Bundle -CandidateServerId $CandidateServerId -Step $Step
    if ($null -eq $item) {
        return $false
    }
    return [string](Get-ObjectProperty $item "status") -eq "ok"
}

function Get-BundleBootEvent {
    param(
        [object]$Bundle,
        [uint32]$BootEventID
    )
    foreach ($event in @(Get-ObjectProperty $Bundle "boot_events")) {
        $rawID = Get-ObjectProperty $event "id"
        if ($null -eq $rawID) {
            continue
        }
        if ([uint32]$rawID -eq $BootEventID) {
            return $event
        }
    }
    return $null
}

function Get-BundleTarget {
    param(
        [object]$Bundle,
        [uint32]$CandidateServerID
    )
    foreach ($target in @(Get-ObjectProperty $Bundle "targets")) {
        $rawServerID = Get-ObjectProperty $target "server_id"
        if ($null -eq $rawServerID) {
            continue
        }
        if ([uint32]$rawServerID -eq $CandidateServerID) {
            return $target
        }
    }
    return $null
}

function Get-TargetSubject {
    param(
        [uint32]$CandidateServerID,
        [object]$Target
    )
    $subject = Get-ObjectProperty $Target "asset_no"
    if ([string]::IsNullOrWhiteSpace($subject)) {
        $subject = Get-ObjectProperty $Target "hostname"
    }
    if ([string]::IsNullOrWhiteSpace($subject)) {
        $subject = "server:$CandidateServerID"
    }
    return $subject
}

function New-ItemEvidenceCandidate {
    param(
        [Parameter(Mandatory = $true)][string]$Kind,
        [uint32]$RunID,
        [object]$ServerID = $null,
        [object]$BootEventID = $null,
        [Parameter(Mandatory = $true)][string]$Subject,
        [Parameter(Mandatory = $true)][string]$Summary,
        [Parameter(Mandatory = $true)][string]$Details
    )
    return [pscustomobject]@{
        Kind        = $Kind
        RunID       = $RunID
        ServerID    = $ServerID
        BootEventID = $BootEventID
        Subject     = $Subject
        Summary     = $Summary
        Details     = $Details
    }
}

function Get-ItemEvidenceCandidates {
    param(
        [object]$Bundle,
        [uint32]$RunID
    )

    $seen = @{}
    $candidates = @()
    foreach ($item in @(Get-ObjectProperty $Bundle "operator_checklist")) {
        if ($null -eq $item) {
            continue
        }
        $step = [string](Get-ObjectProperty $item "step")
        $status = [string](Get-ObjectProperty $item "status")
        if ($status -ne "ok") {
            continue
        }

        if ($step -eq "pxe_boot_event") {
            $rawBootEventID = Get-ObjectProperty $item "boot_event_id"
            if ($null -eq $rawBootEventID -or [uint32]$rawBootEventID -eq 0) {
                continue
            }
            $bootEventID = [uint32]$rawBootEventID
            $event = Get-BundleBootEvent -Bundle $Bundle -BootEventID $bootEventID
            if ($null -eq $event) {
                Write-Host "Skipping PXE evidence for boot_event_id=$bootEventID because it is missing from the evidence bundle."
                continue
            }
            $mac = [string](Get-ObjectProperty $event "mac")
            if ([string]::IsNullOrWhiteSpace($mac)) {
                Write-Host "Skipping PXE evidence for boot_event_id=$bootEventID because the bundle event has no MAC."
                continue
            }
            if (-not (Test-PXEMacRequested $mac)) {
                continue
            }
            $serverID = Get-ObjectProperty $item "server_id"
            if ($null -ne $serverID -and [uint32]$serverID -gt 0 -and -not (Test-ServerRequested -Candidate ([uint32]$serverID))) {
                continue
            }
            $key = "pxe:{0}" -f $bootEventID
            if ($seen.ContainsKey($key)) {
                continue
            }
            $seen[$key] = $true
            $summary = "Physical PXE boot event recorded by tools/physical-validation.ps1"
            $details = "Strict lab validation run $RunID observed physical PXE BootEvent #$bootEventID for MAC $mac."
            if ($null -ne $serverID -and [uint32]$serverID -gt 0) {
                $details = "$details Linked server_id $([uint32]$serverID)."
            }
            $candidates += New-ItemEvidenceCandidate -Kind "pxe" -RunID $RunID -ServerID $serverID -BootEventID $bootEventID -Subject $mac -Summary $summary -Details $details
            continue
        }

        if ($step -ne "bmc_identity" -and $step -ne "ssh_command") {
            continue
        }
        $rawServerID = Get-ObjectProperty $item "server_id"
        if ($null -eq $rawServerID -or [uint32]$rawServerID -eq 0) {
            continue
        }
        $candidateServerID = [uint32]$rawServerID
        if (-not (Test-ServerRequested -Candidate $candidateServerID)) {
            continue
        }
        $target = Get-BundleTarget -Bundle $Bundle -CandidateServerID $candidateServerID
        $subject = Get-TargetSubject -CandidateServerID $candidateServerID -Target $target

        if ($step -eq "bmc_identity") {
            $key = "bmc:{0}" -f $candidateServerID
            if ($seen.ContainsKey($key)) {
                continue
            }
            $seen[$key] = $true
            $summary = "Physical Redfish/IPMI identity evidence recorded by tools/physical-validation.ps1"
            $details = "Strict lab validation run $RunID produced physical Redfish/IPMI identity proof for server_id $candidateServerID."
            $candidates += New-ItemEvidenceCandidate -Kind "bmc" -RunID $RunID -ServerID $candidateServerID -Subject $subject -Summary $summary -Details $details
            continue
        }

        $key = "ssh:{0}" -f $candidateServerID
        if ($seen.ContainsKey($key)) {
            continue
        }
        $seen[$key] = $true
        $summary = "Real SSH known_hosts command evidence recorded by tools/physical-validation.ps1"
        $details = "Strict lab validation run $RunID produced real SSH known_hosts host key proof and command proof for server_id $candidateServerID."
        $candidates += New-ItemEvidenceCandidate -Kind "ssh" -RunID $RunID -ServerID $candidateServerID -Subject $subject -Summary $summary -Details $details
    }
    return $candidates
}

function Get-FullEvidenceCandidates {
    param([object]$Bundle)

    $targets = @(Get-ObjectProperty $Bundle "targets")
    $candidates = @()
    foreach ($target in $targets) {
        $rawServerID = Get-ObjectProperty $target "server_id"
        if ($null -eq $rawServerID) {
            continue
        }
        $candidateServerID = [uint32]$rawServerID
        if (-not (Test-ServerRequested -Candidate $candidateServerID)) {
            continue
        }
        $alreadyReady = Get-ObjectProperty $target "full_chain_ready"
        if ($alreadyReady -eq $true) {
            continue
        }
        $bootEventID = Get-ObjectProperty $target "pxe_boot_event_id"
        if ($null -eq $bootEventID -or [uint32]$bootEventID -eq 0) {
            continue
        }
        if (-not (Test-PXEMacRequested (Get-ObjectProperty $target "primary_mac"))) {
            continue
        }
        $pxeOK = Test-ChecklistOK -Bundle $Bundle -CandidateServerId $candidateServerID -Step "pxe_boot_event"
        $bmcOK = Test-ChecklistOK -Bundle $Bundle -CandidateServerId $candidateServerID -Step "bmc_identity"
        $sshOK = Test-ChecklistOK -Bundle $Bundle -CandidateServerId $candidateServerID -Step "ssh_command"
        if (-not ($pxeOK -and $bmcOK -and $sshOK)) {
            continue
        }
        $candidates += [pscustomobject]@{
            ServerID    = $candidateServerID
            BootEventID = [uint32]$bootEventID
            Subject     = (Get-ObjectProperty $target "asset_no")
            Hostname    = (Get-ObjectProperty $target "hostname")
        }
    }
    return $candidates
}

function Get-BundleEvidenceCandidates {
    param(
        [object]$Bundle,
        [string[]]$Kind = @()
    )

    $rawCandidates = @(Get-ObjectProperty $Bundle "evidence_candidates")
    $candidates = @()
    foreach ($raw in $rawCandidates) {
        if ($null -eq $raw) {
            continue
        }
        $candidateKind = [string](Get-ObjectProperty $raw "kind")
        if ([string]::IsNullOrWhiteSpace($candidateKind)) {
            continue
        }
        if ($Kind.Count -gt 0 -and -not ($Kind -contains $candidateKind)) {
            continue
        }
        $rawRunID = Get-ObjectProperty $raw "run_id"
        if ($null -eq $rawRunID -or [uint32]$rawRunID -eq 0) {
            continue
        }
        $serverID = Get-ObjectProperty $raw "server_id"
        $bootEventID = Get-ObjectProperty $raw "boot_event_id"
        $candidate = [pscustomobject]@{
            Kind        = $candidateKind
            RunID       = [uint32]$rawRunID
            ServerID    = $null
            BootEventID = $null
            Subject     = [string](Get-ObjectProperty $raw "subject")
            Summary     = [string](Get-ObjectProperty $raw "summary")
            Details     = [string](Get-ObjectProperty $raw "details")
        }
        if ($null -ne $serverID -and [uint32]$serverID -gt 0) {
            $candidate.ServerID = [uint32]$serverID
        }
        if ($null -ne $bootEventID -and [uint32]$bootEventID -gt 0) {
            $candidate.BootEventID = [uint32]$bootEventID
        }
        if (-not (Test-EvidenceCandidateRequested -Bundle $Bundle -Candidate $candidate)) {
            continue
        }
        if ([string]::IsNullOrWhiteSpace($candidate.Subject) -or [string]::IsNullOrWhiteSpace($candidate.Summary)) {
            continue
        }
        $candidates += $candidate
    }
    return $candidates
}

function New-ItemEvidencePayload {
    param([object]$Candidate)
    $payload = @{
        kind    = $Candidate.Kind
        status  = "ok"
        subject = $Candidate.Subject
        summary = $Candidate.Summary
        details = $Candidate.Details
        run_id  = [uint32]$Candidate.RunID
    }
    if ($null -ne $Candidate.ServerID -and [uint32]$Candidate.ServerID -gt 0) {
        $payload.server_id = [uint32]$Candidate.ServerID
    }
    if ($null -ne $Candidate.BootEventID -and [uint32]$Candidate.BootEventID -gt 0) {
        $payload.boot_event_id = [uint32]$Candidate.BootEventID
    }
    return $payload
}

function New-FullEvidencePayload {
    param(
        [uint32]$RunID,
        [uint32]$CandidateServerID,
        [uint32]$BootEventID,
        [string]$Subject,
        [string]$Hostname
    )
    $cleanSubject = $Subject
    if ([string]::IsNullOrWhiteSpace($cleanSubject)) {
        $cleanSubject = $Hostname
    }
    if ([string]::IsNullOrWhiteSpace($cleanSubject)) {
        $cleanSubject = "server:$CandidateServerID"
    }
    $details = $EvidenceDetails
    if ([string]::IsNullOrWhiteSpace($details)) {
        $details = "Strict PXE/BMC/SSH run $RunID produced physical PXE BootEvent #$BootEventID, BMC identity proof, and SSH command proof for server_id $CandidateServerID."
    }
    return @{
        kind          = "full"
        status        = "ok"
        subject       = $cleanSubject
        summary       = $EvidenceSummary
        details       = $details
        run_id        = $RunID
        server_id     = $CandidateServerID
        boot_event_id = $BootEventID
    }
}

function Export-EvidenceBundle {
    param(
        [uint32]$RunID,
        [hashtable]$Headers,
        [string]$Prefix
    )
    $bundleUrl = Join-ApiUrl $apiRoot "system/lab-validation/runs/$RunID/evidence-bundle"
    Write-Host "Exporting evidence bundle for run_id=$RunID"
    $bundle = Invoke-JsonRequest -Method "GET" -Url $bundleUrl -Headers $Headers
    Save-JsonFile -Value $bundle -Path (Join-Path $OutDir "$Prefix-evidence-bundle.json")
    return $bundle
}

function Invoke-StrictValidationRun {
    param(
        [hashtable]$Headers,
        [hashtable]$Payload,
        [string]$Prefix
    )
    Write-Host "Running strict lab validation for server_ids=$($ServerId -join ',') pxe_macs=$($PXEMac -join ',')"
    $run = Invoke-JsonRequest -Method "POST" -Url (Join-ApiUrl $apiRoot "system/lab-validation/run") -Headers $Headers -Body $Payload
    Save-JsonFile -Value $run -Path (Join-Path $OutDir "$Prefix-run.json")
    if ($null -eq $run.run_id -or [int]$run.run_id -le 0) {
        throw "Validation run did not return a run_id."
    }
	return $run
}

$ServerId = Normalize-ServerIdArgs $ServerId
$PXEMac = Normalize-PXEMacArgs $PXEMac
Assert-StrictInputs

$root = $BaseUrl.TrimEnd("/")
$apiRoot = Join-ApiUrl $root "api/v1"
$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

Write-Host "Checking readiness at $root/readyz"
$readyz = Invoke-JsonRequest -Method "GET" -Url (Join-ApiUrl $root "readyz")
Save-JsonFile -Value $readyz -Path (Join-Path $OutDir "$timestamp-readyz.json")
if ($readyz.status -ne "ok" -and -not $AllowDegradedReadyz) {
    $messages = @($readyz.checks | Where-Object { $_.status -ne "ok" } | ForEach-Object { "$($_.name): $($_.status) - $($_.message)" })
    throw "Readiness status is $($readyz.status). Re-run with -AllowDegradedReadyz to continue. $($messages -join '; ')"
}

Write-Host "Logging in as $Email"
$password = Read-AdminPassword
try {
    $login = Invoke-JsonRequest -Method "POST" -Url (Join-ApiUrl $apiRoot "auth/login") -Body @{ email = $Email; password = $password }
}
finally {
    $password = $null
}
if ([string]::IsNullOrWhiteSpace($login.token)) {
    throw "Login response did not include a token."
}

$headers = @{
    Authorization      = "Bearer $($login.token)"
    "X-Confirm-Action" = "system.lab-validation.run"
    "X-Request-ID"     = "physical-validation-$timestamp"
}

$payload = @{
	strict    = $true
	check_pxe = $true
	check_bmc = $true
	check_ssh = $true
	server_ids = @(Normalize-ServerIdArgs $ServerId)
	pxe_macs  = @($PXEMac)
	pxe_arch  = $PXEArch
}
if (-not [string]::IsNullOrWhiteSpace($SSHProbeCommand)) {
    $payload.ssh_probe_command = $SSHProbeCommand
}

$run = Invoke-StrictValidationRun -Headers $headers -Payload $payload -Prefix $timestamp

$bundleHeaders = @{
    Authorization  = "Bearer $($login.token)"
    "X-Request-ID" = "physical-validation-bundle-$timestamp"
}
$bundle = Export-EvidenceBundle -RunID ([uint32]$run.run_id) -Headers $bundleHeaders -Prefix $timestamp

if ($RecordEvidence) {
    $itemCandidates = @(Get-BundleEvidenceCandidates -Bundle $bundle -Kind @("pxe", "bmc", "ssh"))
    if ($itemCandidates.Count -eq 0) {
        $itemCandidates = @(Get-ItemEvidenceCandidates -Bundle $bundle -RunID ([uint32]$run.run_id))
    }
    if ($itemCandidates.Count -eq 0) {
        Write-Host "No PXE/BMC/SSH item evidence is ready for automatic recording. Inspect $timestamp-evidence-bundle.json for checklist details."
    } else {
        $evidenceHeaders = @{
            Authorization      = "Bearer $($login.token)"
            "X-Confirm-Action" = "system.lab-validation.evidence"
            "X-Request-ID"     = "physical-validation-item-evidence-$timestamp"
        }
        $itemEvidenceRecords = @()
        foreach ($candidate in $itemCandidates) {
            $evidencePayload = New-ItemEvidencePayload -Candidate $candidate
            $target = ""
            if ($null -ne $candidate.ServerID -and [uint32]$candidate.ServerID -gt 0) {
                $target = " server_id=$([uint32]$candidate.ServerID)"
            }
            if ($null -ne $candidate.BootEventID -and [uint32]$candidate.BootEventID -gt 0) {
                $target = "$target boot_event_id=$([uint32]$candidate.BootEventID)"
            }
            Write-Host "Recording $($candidate.Kind) evidence$target"
            $evidence = Invoke-JsonRequest -Method "POST" -Url (Join-ApiUrl $apiRoot "system/lab-validation/evidence") -Headers $evidenceHeaders -Body $evidencePayload
            $itemEvidenceRecords += $evidence
        }
        Save-JsonFile -Value $itemEvidenceRecords -Path (Join-Path $OutDir "$timestamp-item-evidence.json")
    }
}

if ($RecordFullEvidence) {
    $candidates = @(Get-BundleEvidenceCandidates -Bundle $bundle -Kind @("full"))
    $useBundleCandidates = $candidates.Count -gt 0
    if (-not $useBundleCandidates) {
        $candidates = @(Get-FullEvidenceCandidates -Bundle $bundle)
    }
    if ($candidates.Count -eq 0) {
        Write-Host "No target is ready for automatic full-chain evidence. Inspect $timestamp-evidence-bundle.json for checklist details."
    } else {
        $evidenceHeaders = @{
            Authorization      = "Bearer $($login.token)"
            "X-Confirm-Action" = "system.lab-validation.evidence"
            "X-Request-ID"     = "physical-validation-evidence-$timestamp"
        }
        $evidenceRecords = @()
        foreach ($candidate in $candidates) {
            if ($useBundleCandidates) {
                $evidencePayload = New-ItemEvidencePayload -Candidate $candidate
            } else {
                $evidencePayload = New-FullEvidencePayload -RunID ([uint32]$run.run_id) -CandidateServerID $candidate.ServerID -BootEventID $candidate.BootEventID -Subject $candidate.Subject -Hostname $candidate.Hostname
            }
            Write-Host "Recording full-chain evidence for server_id=$($candidate.ServerID) boot_event_id=$($candidate.BootEventID)"
            $evidence = Invoke-JsonRequest -Method "POST" -Url (Join-ApiUrl $apiRoot "system/lab-validation/evidence") -Headers $evidenceHeaders -Body $evidencePayload
            $evidenceRecords += $evidence
        }
        Save-JsonFile -Value $evidenceRecords -Path (Join-Path $OutDir "$timestamp-full-evidence.json")

        $rerunHeaders = @{
            Authorization      = "Bearer $($login.token)"
            "X-Confirm-Action" = "system.lab-validation.run"
            "X-Request-ID"     = "physical-validation-rerun-$timestamp"
        }
        $rerunPrefix = "$timestamp-rerun"
        $run = Invoke-StrictValidationRun -Headers $rerunHeaders -Payload $payload -Prefix $rerunPrefix
        $bundleHeaders["X-Request-ID"] = "physical-validation-rerun-bundle-$timestamp"
        $bundle = Export-EvidenceBundle -RunID ([uint32]$run.run_id) -Headers $bundleHeaders -Prefix $rerunPrefix
    }
}

$failed = @($run.run_results | Where-Object { $_.status -ne "success" })
Write-Host "Validation status: $($run.status); run_id=$($run.run_id); failed_or_skipped=$($failed.Count)"
foreach ($item in $failed) {
    Write-Host " - $($item.kind) server_id=$($item.server_id) status=$($item.status): $($item.message)"
}

if ($run.status -ne "ok") {
    exit 2
}
