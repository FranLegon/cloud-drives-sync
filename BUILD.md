# Build

Suppress `go-sqlcipher` warnings and cache CGO compilation:

```powershell
$env:CGO_CFLAGS="-Wno-return-local-addr"
go build -o cloud-drives-sync.exe .
```

Pre-build the dependency (one-time, cached afterward):

```powershell
$env:CGO_CFLAGS="-Wno-return-local-addr"
go build github.com/mutecomm/go-sqlcipher/v4
```

Verify cache location:

```powershell
go env GOCACHE
```
