# Suppress go-sqlcipher warnings and cache CGO compilation:

```powershell
$env:CGO_CFLAGS="-Wno-return-local-addr"
go build -o cloud-drives-sync.exe .
#Pre-build the dependency (one-time, cached afterward):
$env:CGO_CFLAGS="-Wno-return-local-addr"
go build github.com/mutecomm/go-sqlcipher/v4
#Verify cache location:
go env GOCACHE
```

# Set `$env:SYNC_CLOUD_DRIVES_PASS`
```powershell
#Set the password for testing (must be the same as in .env):
$env:SYNC_CLOUD_DRIVES_PASS="your_password_here"
```

# Test
```powershell
#Run tests:
go build -o cloud-drives-sync.exe .; .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS
```

# Compare current vs last commit results:
```powershell
#Compare execution time of current code vs last commit:
git stash; git checkout main~1; go build -o cloud-drives-sync.exe .; Write-Host "=== BEFORE ==="; measure-command { .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS }; git checkout main; git stash pop; go build -o cloud-drives-sync.exe .; Write-Host "=== AFTER ==="; measure-command { .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS }   
```