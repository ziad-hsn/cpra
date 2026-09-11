param(
    [Parameter(Mandatory=$true)][string]$Binary,
    [Parameter(Mandatory=$true)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
$Binary = (Resolve-Path $Binary).Path
if (Test-Path $OutputDirectory) { throw 'Choose an absent evidence directory' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$OutputDirectory = (Resolve-Path $OutputDirectory).Path
$utf8 = New-Object System.Text.UTF8Encoding($false)
$state = Join-Path $OutputDirectory 'state'
$manifest = Join-Path $OutputDirectory 'monitors.yaml'
$tokenFile = Join-Path $OutputDirectory 'auth.token'
$token = [Guid]::NewGuid().ToString('N') + [Guid]::NewGuid().ToString('N')
[IO.File]::WriteAllText($manifest,"monitors: []`n",$utf8)
[IO.File]::WriteAllText($tokenFile,$token,$utf8)
$portListener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback,0)
$portListener.Start(); $port = $portListener.LocalEndpoint.Port; $portListener.Stop()
$checks = @()
$owner = $null
$competitor = $null
$report = [ordered]@{status='incomplete'; os=[Environment]::OSVersion.VersionString; architecture=$env:PROCESSOR_ARCHITECTURE; filesystem=(Get-Volume -DriveLetter ([IO.Path]::GetPathRoot($OutputDirectory).Substring(0,1))).FileSystem; sha256=(Get-FileHash -Algorithm SHA256 $Binary).Hash.ToLowerInvariant(); checks=@(); boundary='Native foreground process, authentication, forced termination and durable reopen; does not certify installed SCM lifecycle or power-loss durability.'}

function Start-Owner([string]$label, [switch]$Headless) {
    $p = New-Object Diagnostics.Process
    $p.StartInfo.FileName = $Binary
    # All paths name controlled files/directories, cannot contain a quote, and
    # have no trailing slash. Quote each complete argument for Windows parsing.
    $arguments = @('-yaml',$manifest,'-data-dir',$state,'-allow-empty','-web.auth-file',$tokenFile,'-web.addr',"127.0.0.1:$port")
    # A competing owner must fail on storage ownership, not on the original
    # owner's listener. Headless startup removes that independent failure path.
    if ($Headless) { $arguments += '-web=false' }
    $p.StartInfo.Arguments = (($arguments | ForEach-Object {'"'+$_+'"'}) -join ' ')
    $p.StartInfo.WorkingDirectory = $OutputDirectory
    $p.StartInfo.UseShellExecute = $false
    $p.StartInfo.CreateNoWindow = $true
    $p.StartInfo.RedirectStandardOutput = $true
    $p.StartInfo.RedirectStandardError = $true
    if (-not $p.Start()) { throw 'Could not start CPRa' }
    return [PSCustomObject]@{Process=$p; Stdout=$p.StandardOutput.ReadToEndAsync(); Stderr=$p.StandardError.ReadToEndAsync(); Label=$label}
}
function Finish-Owner($running) {
    if (-not $running.Process.HasExited) { $running.Process.Kill() }
    if (-not $running.Process.WaitForExit(10000)) { throw 'Test process did not terminate' }
    if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($running.Stdout,$running.Stderr),10000)) {
        throw 'Test process output streams did not close after termination'
    }
    $running.Stdout.Result | Set-Content -Encoding UTF8 (Join-Path $OutputDirectory ($running.Label+'.stdout.txt'))
    $running.Stderr.Result | Set-Content -Encoding UTF8 (Join-Path $OutputDirectory ($running.Label+'.stderr.txt'))
    $running.Process.Dispose()
}
function Wait-Ready($running) {
    $deadline=(Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        if ($running.Process.HasExited) { throw "CPRa exited during initialization: $($running.Process.ExitCode) $($running.Stderr.Result)" }
        try {
            $r=Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/api/v1/readyz" -Headers @{Authorization="Bearer $token"} -TimeoutSec 2
            if ($r.StatusCode -eq 200) { return }
        } catch {}
        Start-Sleep -Milliseconds 100
    }
    throw 'CPRa readiness timeout'
}
try {
    $owner=Start-Owner 'initial'; Wait-Ready $owner
    $identity=(Get-Content -Raw (Join-Path $state 'identity.json') | ConvertFrom-Json).id
    if (-not $identity) { throw 'Durable identity missing' }
    $checks += 'initialized_ready_with_intentional_empty_manifest'
    $rejected=$false
    try { Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/api/v1/readyz" -Headers @{Authorization='Bearer deliberately-invalid'} -TimeoutSec 2 | Out-Null } catch { $rejected=$_.Exception.Response.StatusCode.value__ -eq 401 }
    if (-not $rejected) { throw 'Wrong token was not rejected' }
    $checks += 'authentication_enforced'
    $competitor=Start-Owner 'competing-owner' -Headless
    if (-not $competitor.Process.WaitForExit(10000)) { throw 'Second owner did not fail promptly' }
    if ($competitor.Process.ExitCode -eq 0) { throw 'Second owner succeeded unexpectedly' }
    if ($competitor.Stderr.Result -notmatch 'durable startup: open durable directory \(locked or corrupt\):') {
        throw 'Second owner failed for a reason other than durable storage ownership'
    }
    Finish-Owner $competitor; $competitor=$null
    $checks += 'second_owner_rejected'
    Finish-Owner $owner; $owner=$null
    $checks += 'real_process_forced_termination'
    $owner=Start-Owner 'restarted'; Wait-Ready $owner
    $restored=(Get-Content -Raw (Join-Path $state 'identity.json') | ConvertFrom-Json).id
    if ($restored -ne $identity) { throw 'Restart replaced durable identity' }
    $checks += 'restart_retains_identity_and_readiness'
    $report.status='pass'; $report.node_id=$identity
} catch {
    $report.status='fail'; $report.error=$_.Exception.Message
} finally {
    foreach ($remaining in @($competitor, $owner)) {
        if ($null -ne $remaining) {
            try { Finish-Owner $remaining } catch {
                $report.status='fail'; $report.cleanup_error=$_.Exception.Message
            }
        }
    }
    $report.checks=$checks
    $report.finished_utc=(Get-Date).ToUniversalTime().ToString('o')
    # Keep synthetic credentials out of the archived evidence set.
    try { Remove-Item -LiteralPath $tokenFile -ErrorAction Stop } catch {
        $report.status='fail'; $report.cleanup_error=$_.Exception.Message
    }
    $report | ConvertTo-Json -Depth 6 | Set-Content -Encoding UTF8 (Join-Path $OutputDirectory 'native-runtime.json')
}
if ($report.status -ne 'pass') { throw $report.error }
$report | ConvertTo-Json -Depth 6
