$mainPrompt = "Test case '$lastFailedTestCase' failed, fix the bug. You can see agents\new_version\SPEC.md if further clarification is needed."
$gitClarification = "`nBefore you finish, write a single-line conventional commit message (e.g. 'fix: tc-$lastFailedTestCase') summarizing your changes to the file .commitmsg in the repo root."
$prompt = $mainPrompt + $gitClarification

Set-Location 'C:\Users\francisco.legon\GitHub\IMEMINE\cloud-drives-sync'
Get-Content .env | ForEach-Object { if ($_ -match '^(.*?)=(.*)$') { Set-Item -Path "Env:$($Matches[1])" -Value $Matches[2] } }

# Enforce git restrictions via opencode.json permissions (deny mutating git in all shells)
$opencodeConfig = Get-Content opencode.json | ConvertFrom-Json
$gitRules = [ordered]@{
    'git status*'       = 'allow'
    'git diff*'         = 'allow'
    'git log*'          = 'allow'
    'git show*'         = 'allow'
    '*git commit*'      = 'deny'
    '*git push*'        = 'deny'
    '*git pull*'        = 'deny'
    '*git reset*'       = 'deny'
    '*git rebase*'      = 'deny'
    '*git merge*'       = 'deny'
    '*git checkout*'    = 'deny'
    '*git switch*'      = 'deny'
    '*git branch*'   = 'deny'
    '*git stash*'       = 'deny'
    '*git cherry-pick*' = 'deny'
    '*git revert*'      = 'deny'
    '*git tag*'         = 'deny'
    '*git am*'          = 'deny'
    '*git restore*'     = 'deny'
    '*git rm*'          = 'deny'
    '*git clean*'       = 'deny'
    '*git filter-branch*' = 'deny'
    '*git update-ref*'  = 'deny'
    '*git replace*'     = 'deny'
    '*git reflog expire*' = 'deny'
    '*git gc*'          = 'deny'
    '*git prune*'       = 'deny'
    '*git apply*'       = 'deny'
    '*git init*'        = 'deny'
    '*git bisect*'      = 'deny'
    '*git submodule*'     = 'deny'
    '*git config*'        = 'deny'
    '*git credential*'      = 'deny'
    '*git archive*'         = 'deny'
    '*git remote*'          = 'deny'
    '*git add*'              = 'deny'
    '*git mv*'               = 'deny'
}
$permission = [ordered]@{
    bash       = $gitRules
    powershell = $gitRules
    pwsh       = $gitRules
    cmd        = [ordered]@{ '*git*' = 'deny' }
}
if (-not $opencodeConfig.permission) {
    $opencodeConfig | Add-Member -MemberType NoteProperty -Name 'permission' -Value $permission
} else {
    $opencodeConfig.permission = $permission
}
$opencodeConfig | ConvertTo-Json -Depth 10 | Set-Content opencode.json

$model = 'google-vertex/gemini-3.1-pro-preview'

function Reset-WorkingTree {
    git checkout main --force | Out-Null
    git clean -fd | Out-Null
    Remove-Item .commitmsg -ErrorAction SilentlyContinue
    if (git stash list) { git stash pop | Out-Null }
}

$maxIterations = 50
$iteration = 1
$sessionMessages = 1
$maxSessionMessages = 4
$opencodeTimeoutMs = 7200000 # 2 hours
while ($iteration -le $maxIterations) {
    # Banner: set terminal tab title (survives TUI apps like opencode) + ANSI row-1 banner (visible between commands)
    $trimmed = $mainPrompt.Trim() -replace '\s+', ' '
    $short = $trimmed.Substring(0, [Math]::Min(200, $trimmed.Length)) + $(if ($trimmed.Length -gt 200) {'...'})
    $banner = "[$iteration/$maxIterations] $short"
    Write-Host -NoNewline "`e]0;$banner`a"
    Write-Host -NoNewline "`e[s`e[H`e[K`e[7m $banner `e[0m`e[u"

    if ($prompt -notmatch [regex]::Escape($gitClarification)) {
        Write-Host "Prompt is missing git clarification. Resetting prompt to include it." -ForegroundColor Yellow
        $prompt = $mainPrompt + $gitClarification
    }
    # abort if prompt keeps failing
    if ($sessionMessages -ge $maxSessionMessages) {
        Write-Host "Too many consecutive failed attempts ($sessionMessages). Resetting to a new prompt." -ForegroundColor Red
        Reset-WorkingTree
        $iteration++
        Write-Host "Next iteration focus: $mainPrompt" -ForegroundColor Cyan
        $prompt = ($mainPrompt + $gitClarification)
    }
    # run OpenCode with timeout
    if ($prompt -eq ($mainPrompt + $gitClarification)) {
        $sessionMessages = 1
        $proc = Start-Process opencode -ArgumentList @("run", $prompt, "--model", $model) -NoNewWindow -PassThru
    } else {
        $sessionMessages++
        $proc = Start-Process opencode -ArgumentList @("run", "-c", $prompt) -NoNewWindow -PassThru
    }
    $timeoutHours = $opencodeTimeoutMs / 3600000
    if (-not $proc.WaitForExit($opencodeTimeoutMs)) {
        Write-Host "OpenCode timed out after ${timeoutHours}h. Resetting..." -ForegroundColor Red
        $proc.Kill()
        Reset-WorkingTree
        $prompt = $mainPrompt + $gitClarification
        continue
    }
    if ($proc.ExitCode -ne 0) { 
        Write-Host "OpenCode execution failed. Checking out to main and resetting to discard any changes..." -ForegroundColor Red
        Reset-WorkingTree
        $prompt = $mainPrompt + $gitClarification
        continue
    }
    
    go build -o cloud-drives-sync.exe . | Tee-Object -Variable buildOutput
    if ($LASTEXITCODE -ne 0) { 
        Write-Host "Build failed. Output:" -ForegroundColor Red
        $prompt = "The build failed with the following output: $buildOutput. Analyze the error and fix it before proceeding." + $gitClarification
        continue
    }
    # Read AI-generated commit message if available
    if ($lastFailedTestCase) {
        .\cloud-drives-sync.exe test --force -p $env:CLOUD_DRIVES_SYNC_PASS --with-commit --case "$lastFailedTestCase" | Tee-Object -Variable testOutput
    } else {
        .\cloud-drives-sync.exe test --force -p $env:CLOUD_DRIVES_SYNC_PASS --with-commit | Tee-Object -Variable testOutput
    }
    Remove-Item .commitmsg -ErrorAction SilentlyContinue
    
    $testExitCode = $LASTEXITCODE
    $testErrorLines = Get-Content test.log | Where-Object { $_ -match "ERROR|FATAL|PANIC"}
    if ($testExitCode -ne 0) { 
        Write-Host "Tests failed. Output:" -ForegroundColor Red
        $prompt = "The tests failed with the following output: $testOutput. Your .go changes are still in the working tree (uncommitted). Analyze the error and fix them before proceeding." + $gitClarification
    } elseif ($testErrorLines) {
        $lastFailedTestCase = $null
        Write-Host "Tests passed but errors were found in logs. Lines:" -ForegroundColor Yellow
        $testErrorLines | ForEach-Object { Write-Host $_ -ForegroundColor Yellow }
        $prompt = "The tests passed (changes already committed) but the following errors were found in the logs:`n$($testErrorLines -join '; ').`nAnalyze these errors and fix them before proceeding." + $gitClarification
    } else {
        $lastFailedTestCase = $null
        git push | Out-Null
        Write-Host "Build and tests succeeded without errors. Pushed." -ForegroundColor Green
        $iteration++
        Write-Host "Next iteration focus: $mainPrompt" -ForegroundColor Cyan
        $prompt = $mainPrompt + $gitClarification
    }
    $lastFailedTestCase = Get-Content test.log |
    ForEach-Object {
        if ($_ -match "ERROR: Test failed: SPEC test case (\d+)") {
            $Matches[1]
        }
    } | Select-Object -Last 1
}