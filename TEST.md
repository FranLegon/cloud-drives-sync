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

# Set `$env:CLOUD_DRIVES_SYNC_PASS`
```powershell
#Set the password for testing in .env:
$env:CLOUD_DRIVES_SYNC_PASS="your_password_here"
if (-not (Test-Path -Path .env)) { New-Item -Path .env -ItemType File -Value "CLOUD_DRIVES_SYNC_PASS=$($env:CLOUD_DRIVES_SYNC_PASS)" }
if (-not (Select-String -Path .gitignore -Pattern "^\.env$" -Quiet)) { Add-Content -Path .gitignore -Value ".env" }
```

# Test
```powershell
#Load .env variables into environment:
Get-Content .env | ForEach-Object { if ($_ -match '^(.*?)=(.*)$') { Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2] } }
#Run tests:
go build -o cloud-drives-sync.exe . && .\cloud-drives-sync.exe test --force -p $env:CLOUD_DRIVES_SYNC_PASS --with-commit
```

# Test specific test case
```powershell
#Load .env variables into environment:
Get-Content .env | ForEach-Object { if ($_ -match '^(.*?)=(.*)$') { Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2] } }
#Run specific test case:
go build -o cloud-drives-sync.exe . && .\cloud-drives-sync.exe test --force -p $env:CLOUD_DRIVES_SYNC_PASS --test-case "TestCaseNumber"
```

<!--
# Compare current vs last commit results:
```powershell
#Compare execution time of current code vs last commit:
git stash && git checkout main~1 && go build -o cloud-drives-sync.exe . && Write-Host "=== BEFORE ===" && measure-command { .\cloud-drives-sync.exe test --force -p $env:CLOUD_DRIVES_SYNC_PASS } && write-host (Get-ChildItem -Path logs -File | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | Select-Object -ExpandProperty FullName) && git checkout main && git stash pop && go build -o cloud-drives-sync.exe . && Write-Host "=== AFTER ===" && measure-command { .\cloud-drives-sync.exe test --force -p $env:CLOUD_DRIVES_SYNC_PASS } && write-host (Get-ChildItem -Path logs -File | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | Select-Object -ExpandProperty FullName)
```
-->

# OpenCode infite loop:
```powershell
<<<<<<< Updated upstream
clear ; & 'C:\Users\francisco.legon\GitHub\IMEMINE\cloud-drives-sync\agents\infinite-loop.ps1'
=======
$mainPrompt = @"
Look for possible optimizations. 
Find the single highest-impact, low-risk improvement.
Make only focused changes that are clearly justified.
Preserve behavior unless a bug fix is explicitly needed.
The only change allowed for cmd\test.go is adding more logs, so focus on the other go files instead.
Do not run tests yourself. I will build and run tests after you finish.
"@
$prompt = $mainPrompt

cd 'C:\Users\francisco.legon\GitHub\IMEMINE\cloud-drives-sync'

# Enforce git restrictions via opencode.json permissions (blocks mutating git commands in any shell)
$opencodeConfig = Get-Content opencode.json | ConvertFrom-Json
$opencodeConfig | Add-Member -Force -MemberType NoteProperty -Name 'permission' -Value ([ordered]@{
    bash = [ordered]@{
        '*'               = 'allow'
        'git status*'     = 'allow'
        'git diff*'       = 'allow'
        'git log*'        = 'allow'
        'git show*'       = 'allow'
        'git commit*'     = 'deny'
        'git push*'       = 'deny'
        'git reset*'      = 'deny'
        'git rebase*'     = 'deny'
        'git merge*'      = 'deny'
        'git checkout*'   = 'deny'
        'git switch*'     = 'deny'
        'git branch -d*'  = 'deny'
        'git branch -D*'  = 'deny'
        'git stash*'      = 'deny'
        'git cherry-pick*'= 'deny'
        'git revert*'     = 'deny'
        'git tag*'        = 'deny'
        'git am*'         = 'deny'
        'powershell*git*' = 'deny'
        'pwsh*git*'       = 'deny'
        'cmd*git*'        = 'deny'
    }
})
$opencodeConfig | ConvertTo-Json -Depth 10 | Set-Content opencode.json

$model = 'google-vertex/gemini-3.1-pro-preview'

$maxIterations = 10
$iteration = 1
while ($iteration -le $maxIterations) {
    if ($prompt -eq $mainPrompt) {
        opencode run $prompt --model $model
    } else {
        opencode run -c $prompt 
    }
    
    
    go build -o cloud-drives-sync.exe . | Tee-Object -Variable buildOutput
    if ($LASTEXITCODE -ne 0) { 
        Write-Host "Build failed. Output:" -ForegroundColor Red
        $prompt = "The build failed with the following output: $buildOutput. Analyze the error and fix it before proceeding."
        continue
    }
    .\cloud-drives-sync.exe test --force -p $env:CLOUD_DRIVES_SYNC_PASS --with-commit | Tee-Object -Variable testOutput
    $testExitCode = $LASTEXITCODE
    $testErrorLines = Get-Content test.log | Where-Object { $_ -match "ERROR|FATAL|PANIC"}
    if ($testExitCode -ne 0) { 
        Write-Host "Tests failed. Output:" -ForegroundColor Red
        $prompt = "The tests failed with the following output: $testOutput. Your .go changes are still in the working tree (uncommitted). Analyze the error and fix them before proceeding."
    } elseif ($testErrorLines) {
        Write-Host "Tests passed but errors were found in logs. Lines:" -ForegroundColor Yellow
        $testErrorLines | ForEach-Object { Write-Host $_ -ForegroundColor Yellow }
        $prompt = "The tests passed (changes already committed) but the following errors were found in the logs:`n$($testErrorLines -join '; ').`nAnalyze these errors and fix them before proceeding."
    } else {
        Write-Host "Build and tests succeeded without errors." -ForegroundColor Green
        $prompt = $mainPrompt
        $iteration++
    }
}
>>>>>>> Stashed changes
```
