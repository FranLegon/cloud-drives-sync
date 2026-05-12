
$focusPrompts = @(
    @{ Weight = 4;  Prompt = "Tighten command orchestration flow. Simplify repeated pre-check and setup logic between commands, reduce branching complexity, and keep exact user-visible behavior unless fixing a clear bug." }
    @{ Weight = 4;  Prompt = "Harden error handling paths across Go files. Prioritize wrapping errors with context, avoiding swallowed errors, and returning actionable messages. Preserve existing behavior and public interfaces." }
    @{ Weight = 4;  Prompt = "Reduce unnecessary API calls and repeated cloud lookups. Cache short-lived results within command execution when safe, and remove duplicate fetch patterns. Do not change sync semantics." }
    @{ Weight = 4;  Prompt = "Update all .md files in the repository to reflect the current state of the codebase. Ensure READMEs and documentation match the actual logic and CLI flags." }
    @{ Weight = 4;  Prompt = "Improve reliability of retry and backoff usage. Ensure transient network and rate-limit errors use consistent retry strategy and avoid retrying non-retriable errors." }
    @{ Weight = 4;  Prompt = "Look for possible refactors to avoid repeating code, simplify complex logic, improve naming, and remove redundancy.
               Make sure not to alter the behavior of the code unless it's to fix a bug. Focus on improving code quality and maintainability.
               If you think no refactor is necessary, don't do anything." }
    @{ Weight = 4;  Prompt = "Unify duplicate path normalization and file identity logic. Consolidate repeated helpers for path cleaning, calculated_id handling, and provider naming consistency without changing outputs." }
    @{ Weight = 4;  Prompt = "Strengthen database interaction safety. Focus on consistent transaction boundaries, prepared statements reuse, and clearer failure handling while preserving schema and command behavior." }
    @{ Weight = 4;  Prompt = "Improve logging signal-to-noise. Keep current log style, but make critical actions and failures more diagnosable with concise context (provider, account, path, native_id) and fewer redundant lines." }
    @{ Weight = 4;  Prompt = "Optimize memory and stream handling in upload/download paths. Remove avoidable buffering and ensure readers/writers are closed correctly in all branches, including error paths." }
    @{ Weight = 8;  Prompt = "Ensure the sync command is idempotent and can recover from an interrupted run (e.g. crash or reboot mid-sync). Focus on checkpointing progress in the database so already-synced files are not re-processed. Do not change sync semantics." }
    @{ Weight = 7;  Prompt = "Audit sync and API call paths for proper handling of 429 Too Many Requests and transient server errors. Ensure rate-limit responses trigger backoff/retry rather than failing the entire sync. Do not change retry logic for non-retriable errors." }
    @{ Weight = 60; Prompt = @"
Look for possible optimizations. 
Find the single highest-impact, low-risk improvement.
Make only focused changes that are clearly justified.
Preserve behavior unless a bug fix is explicitly needed.
The only change allowed for cmd\test.go is adding more logs, so focus on the other go files instead.
Do not run tests yourself. I will build and run tests after you finish.
"@ }
)

function Select-WeightedPrompt {
    $totalWeight = ($focusPrompts | Measure-Object -Property Weight -Sum).Sum
    $roll = Get-Random -Minimum 0 -Maximum $totalWeight
    $cumulative = 0
    foreach ($entry in $focusPrompts) {
        $cumulative += $entry.Weight
        if ($roll -lt $cumulative) { return $entry.Prompt }
    }
    return $focusPrompts[-1].Prompt
}

$mainPrompt = Select-WeightedPrompt
$gitClarification = "`nDo not commit or run any git state-changing (mutating) operations (you can still run status/diff/log/show if needed).`nBefore you finish, write a single-line conventional commit message (e.g. 'fix: ...', 'refactor: ...', 'perf: ...') summarizing your changes to the file .commitmsg in the repo root."
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

$maxIterations = 50
$iteration = 1
$sessionMessages = 1
$maxSessionMessages = 4
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
    if ($sessionMessages -gt $maxSessionMessages) {
        Write-Host "Too many consecutive failed attempts. Aborting loop to prevent infinite failures." -ForegroundColor Red
        # Force git checkout main and pop stash to clean up any potential issues before exiting
        git checkout main --force | Out-Null
        git clean -fd | Out-Null
        Remove-Item .commitmsg -ErrorAction SilentlyContinue
        if (git stash list) { git stash pop | Out-Null }
        $prompt = ($mainPrompt + $gitClarification)
    }
    # run OpenCode
    if ($prompt -eq ($mainPrompt + $gitClarification)) {
        $sessionMessages = 1
        opencode run $prompt --model $model
    } else {
        $sessionMessages++
        opencode run -c $prompt 
    }
    if (-not $? -or $LASTEXITCODE -ne 0) { 
        Write-Host "OpenCode execution failed. Checking out to main and resetting to discard any changes..." -ForegroundColor Red
        git checkout main --force | Out-Null
        git clean -fd | Out-Null
        Remove-Item .commitmsg -ErrorAction SilentlyContinue
        if (git stash list) { git stash pop | Out-Null }
        $prompt = $mainPrompt + $gitClarification
        continue
    }
    
    go build -o cloud-drives-sync.exe . | Tee-Object -Variable buildOutput
    if ($LASTEXITCODE -ne 0) { 
        Write-Host "Build failed. Output:" -ForegroundColor Red
        $prompt = "The build failed with the following output: $buildOutput. Analyze the error and fix it before proceeding." + $gitClarification
        continue
    }
    # Commit .md file changes before test — .md files don't require a test run
    if ($prompt -match [regex]::Escape('Update all .md files in the repository')) {
        $mdFiles = git status --porcelain | Where-Object { $_ -match '\.md$' } | ForEach-Object { $_.Substring(3) }
        if ($mdFiles) {
            git add $mdFiles | Out-Null
            git commit -m "docs: update .md files" | Out-Null
            Write-Host "Committed updated .md files: $($mdFiles -join ', ')" -ForegroundColor Green
        } else {
            Write-Host "No .md files were modified to commit." -ForegroundColor Yellow
        }
        $iteration++
        $mainPrompt = Select-WeightedPrompt
        Write-Host "Next iteration focus: $mainPrompt" -ForegroundColor Cyan
        $prompt = $mainPrompt + $gitClarification
        continue
    }
    # Read AI-generated commit message if available
    $commitMsg = if (Test-Path .commitmsg) { (Get-Content .commitmsg -Raw).Trim() } else { "" }
    Remove-Item .commitmsg -ErrorAction SilentlyContinue
    if ($commitMsg) {
        .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS "--with-commit=$commitMsg" | Tee-Object -Variable testOutput
    } else {
        .\cloud-drives-sync.exe test --force -p $env:SYNC_CLOUD_DRIVES_PASS --with-commit | Tee-Object -Variable testOutput
    }
    $testExitCode = $LASTEXITCODE
    $testErrorLines = Get-Content test.log | Where-Object { $_ -match "ERROR|FATAL|PANIC"}
    if ($testExitCode -ne 0) { 
        Write-Host "Tests failed. Output:" -ForegroundColor Red
        $prompt = "The tests failed with the following output: $testOutput. Your .go changes are still in the working tree (uncommitted). Analyze the error and fix them before proceeding." + $gitClarification
    } elseif ($testErrorLines) {
        Write-Host "Tests passed but errors were found in logs. Lines:" -ForegroundColor Yellow
        $testErrorLines | ForEach-Object { Write-Host $_ -ForegroundColor Yellow }
        $prompt = "The tests passed (changes already committed) but the following errors were found in the logs:`n$($testErrorLines -join '; ').`nAnalyze these errors and fix them before proceeding." + $gitClarification
    } else {
        Write-Host "Build and tests succeeded without errors." -ForegroundColor Green
        $iteration++
        $mainPrompt = Select-WeightedPrompt
        Write-Host "Next iteration focus: $mainPrompt" -ForegroundColor Cyan
        $prompt = $mainPrompt + $gitClarification
    }
}