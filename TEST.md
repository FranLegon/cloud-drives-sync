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
go build -o cloud-drives-sync.exe . && .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS --with-commit
```

<!--
# Compare current vs last commit results:
```powershell
#Compare execution time of current code vs last commit:
git stash && git checkout main~1 && go build -o cloud-drives-sync.exe . && Write-Host "=== BEFORE ===" && measure-command { .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS } && write-host (Get-ChildItem -Path logs -File | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | Select-Object -ExpandProperty FullName) && git checkout main && git stash pop && go build -o cloud-drives-sync.exe . && Write-Host "=== AFTER ===" && measure-command { .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS } && write-host (Get-ChildItem -Path logs -File | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | Select-Object -ExpandProperty FullName)
```
-->

# OpenCode infite loop:
```powershell
clear ; & 'C:\Users\francisco.legon\GitHub\IMEMINE\cloud-drives-sync\agents\infinite-loop.ps1'
```
