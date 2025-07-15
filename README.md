# cloud-drives-sync
```
winget install -e --id MSYS2.MSYS2
- In "MSYS2 MSYS" terminal:
- pacman -Syu
- pacman -S mingw-w64-x86_64-gcc

########################################

[Environment]::SetEnvironmentVariable(
  "Path",
  $env:Path + ";C:\msys64\mingw64\bin",
  [EnvironmentVariableTarget]::User
)

########################################

$env:CGO_ENABLED=1
go build -o bin/cloud-drives-sync.exe cloud-drives-sync

########################################

& bin\cloud-drives-sync.exe init
```