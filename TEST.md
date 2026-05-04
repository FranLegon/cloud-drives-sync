# Suppress go-sqlcipher warnings and cache CGO compilation:

```powershell
#Pre-build the dependency (one-time, cached afterward):
$env:CGO_CFLAGS="-Wno-return-local-addr"
go build github.com/mutecomm/go-sqlcipher/v4
#Verify cache location:
go env GOCACHE
#Set CGO_CFLAGS in .env for future use:
if (-not (Test-Path -Path .env)) { New-Item -Path .env -ItemType File -Value "CGO_CFLAGS=$($env:CGO_CFLAGS)" }
if (-not (Select-String -Path .gitignore -Pattern "^\.env$" -Quiet)) { Add-Content -Path .gitignore -Value ".env" }
```

# Set `$env:SYNC_CLOUD_DRIVES_PASS`
```powershell
#Set the password for testing in .env:
$env:SYNC_CLOUD_DRIVES_PASS="your_password_here"
if (-not (Test-Path -Path .env)) { New-Item -Path .env -ItemType File -Value "SYNC_CLOUD_DRIVES_PASS=$($env:SYNC_CLOUD_DRIVES_PASS)" }
if (-not (Select-String -Path .gitignore -Pattern "^\.env$" -Quiet)) { Add-Content -Path .gitignore -Value ".env" }
```

# Test
```powershell
#Load .env variables into environment:
Get-Content .env | ForEach-Object { if ($_ -match '^(.*?)=(.*)$') { Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2] } }
#Run tests:
go build -o cloud-drives-sync.exe . && .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS
```

# Compare current vs last commit results:
```powershell
#Compare execution time of current code vs last commit:
git stash && git checkout main~1 && go build -o cloud-drives-sync.exe . && Write-Host "=== BEFORE ===" && measure-command { .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS } && write-host (Get-ChildItem -Path logs -File | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | Select-Object -ExpandProperty FullName) && git checkout main && git stash pop && go build -o cloud-drives-sync.exe . && Write-Host "=== AFTER ===" && measure-command { .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS } && write-host (Get-ChildItem -Path logs -File | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | Select-Object -ExpandProperty FullName)
```

# opencode infite loop:
```powershell
$mainPrompt = @"
Look for more possible optimizations. 
Find the single highest-impact, low-risk improvement.
Make only focused changes that are clearly justified.
Preserve behavior unless a bug fix is explicitly needed.
The only change allowed for cmd\test.go is adding more logs, so focus on the other go files instead.
If you make changes, run tests as described in TEST.md.
Consider tests take more than 15 minutes to run, be patient and wait for them to finish before analyzing results (you can check timestamped log in logs dir).
Do not commit or run any git operations.
"@
$prompt = $mainPrompt

cd 'C:\Users\francisco.legon\GitHub\IMEMINE\cloud-drives-sync'

$maxIterations = 10
$iteration = 1
while ($iteration -le $maxIterations) {
    opencode run -c $prompt 
    
    go build -o cloud-drives-sync.exe . | Tee-Object -Variable buildOutput
    if ($LASTEXITCODE -ne 0) { 
        Write-Host "Build failed. Output:" -ForegroundColor Red
        $prompt += "The build failed with the following output: $buildOutput. Analyze the error and fix it before proceeding."
        continue
    }
    .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS | Tee-Object -Variable testOutput
    $testExitCode = $LASTEXITCODE
    $testErrorLines = Get-Content test.log | Where-Object { "`nERROR" -in $_ }
    if ($testExitCode -ne 0) { 
        Write-Host "Tests failed. Output:" -ForegroundColor Red
        $prompt += "The tests failed with the following output: $testOutput. Analyze the error and fix it before proceeding."
        continue
    } elseif ($testErrorLines) {
        Write-Host "Tests passed but errors were found in logs. Lines:" -ForegroundColor Yellow
        $testErrorLines | ForEach-Object { Write-Host $_ -ForegroundColor Yellow }
        $prompt += "The tests passed but the following errors were found in the logs: $($testErrorLines -join '; '). Analyze these errors and fix them before proceeding."
        continue
    } else {
        Write-Host "Build and tests succeeded without errors." -ForegroundColor Green
        $commitPrompt = "The build and tests succeeded without errors. Commit the changes with message 'INFINITE OPENCODE:\nSUMMARY: <1-3 lines>\nEVIDENCE: <what improved and why>'"
        opencode run -c $commitPrompt
        $prompt = $mainPrompt
    }
    $iteration++
}
```
