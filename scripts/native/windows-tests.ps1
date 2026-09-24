param(
    [Parameter(Mandatory=$true)][string]$BinaryDirectory,
    [Parameter(Mandatory=$true)][string]$OutputDirectory,
    [ValidateRange(1,3600)][int]$TimeoutSeconds = 180
)
$ErrorActionPreference = 'Stop'
$BinaryDirectory = (Resolve-Path $BinaryDirectory).Path
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
$OutputDirectory = (Resolve-Path $OutputDirectory).Path
$results = @()
foreach ($name in @('cpra-localadmin-windows.test.exe','cpra-persistence-windows.test.exe','cpra-runtime-windows.test.exe')) {
    $binary = Join-Path $BinaryDirectory $name
    $p = New-Object System.Diagnostics.Process
    $p.StartInfo.FileName = $binary
    $p.StartInfo.Arguments = '-test.v -test.timeout=120s'
    $p.StartInfo.WorkingDirectory = $OutputDirectory
    $p.StartInfo.UseShellExecute = $false
    $p.StartInfo.CreateNoWindow = $true
    $p.StartInfo.RedirectStandardOutput = $true
    $p.StartInfo.RedirectStandardError = $true
    $start = Get-Date
    if (-not $p.Start()) { throw "Could not start $name" }
    $stdout = $p.StandardOutput.ReadToEndAsync()
    $stderr = $p.StandardError.ReadToEndAsync()
    $complete = $p.WaitForExit($TimeoutSeconds * 1000)
    if (-not $complete) {
        # Only this test process and its child helpers are terminated.
        & taskkill.exe /PID $p.Id /T /F | Out-Null
        if (-not $p.WaitForExit(10000)) { throw "$name could not be terminated after its deadline" }
    }
    if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdout,$stderr),10000)) {
        throw "$name output streams did not close after process termination"
    }
    $exitCode = $p.ExitCode
    $stdout.Result | Set-Content -Encoding UTF8 (Join-Path $OutputDirectory ($name+'.stdout.txt'))
    $stderr.Result | Set-Content -Encoding UTF8 (Join-Path $OutputDirectory ($name+'.stderr.txt'))
    $passed = $complete -and ($exitCode -eq 0) -and ($stdout.Result -match '(?m)^PASS\r?$')
    $results += [PSCustomObject]@{
        name=$name; status=$(if($passed){'pass'}else{'fail'}); exit_code=$exitCode
        completed=$complete; seconds=((Get-Date)-$start).TotalSeconds
        sha256=(Get-FileHash -Algorithm SHA256 $binary).Hash.ToLowerInvariant()
    }
    Write-Output "$name exit=$exitCode status=$passed"
    $p.Dispose()
}
$report = [PSCustomObject]@{
    status=$(if(@($results | Where-Object {$_.status -ne 'pass'}).Count -eq 0){'pass'}else{'fail'})
    os=[Environment]::OSVersion.VersionString
    architecture=$env:PROCESSOR_ARCHITECTURE
    filesystem=(Get-Volume -DriveLetter ([IO.Path]::GetPathRoot($OutputDirectory).Substring(0,1))).FileSystem
    finished_utc=(Get-Date).ToUniversalTime().ToString('o')
    tests=$results
    boundary='Native Windows test executables; SCM handler unit tests do not certify installed service lifecycle.'
}
$report | ConvertTo-Json -Depth 6 | Set-Content -Encoding UTF8 (Join-Path $OutputDirectory 'native-tests.json')
if ($report.status -ne 'pass') { exit 1 }
