# Cloud Drives Sync — Rebuild Specification

This folder describes **what** the rebuilt `cloud-drives-sync` tool must achieve. It deliberately
avoids prescribing **how** to build it. Treat every document here as a statement of goals,
behaviors, constraints, and acceptance criteria — not as an implementation guide.

## How to use this specification

- Read the documents in order. Each one builds on the concepts of the previous.
- Everything stated here is a **requirement** unless explicitly marked as a non-goal or an example.
- You are free to choose the programming language, libraries, frameworks, storage engine,
  package structure, algorithms, and internal architecture — **as long as every requirement and
  acceptance criterion in these documents is met.**
- When a requirement and an example appear to conflict, the requirement wins. Examples illustrate
  intent; they are not contracts.
- If a requirement is ambiguous, prefer the interpretation that protects user data and avoids
  irreversible loss.

## Document index

| File | Purpose |
|---|---|
| [01-goals-and-principles.md](01-goals-and-principles.md) | The mission, the value it delivers, and the guiding principles. |
| [02-core-concepts.md](02-core-concepts.md) | Vocabulary and the mental model: accounts, sync scope, logical vs physical files. |
| [03-providers.md](03-providers.md) | Per-provider behavior and the invariants each must uphold. |
| [04-data-and-state.md](04-data-and-state.md) | What state the tool must track to do its job correctly. |
| [05-security-and-configuration.md](05-security-and-configuration.md) | Trust, secrets, encryption, and configuration goals. |
| [06-commands.md](06-commands.md) | The user-facing capabilities and what each must accomplish. |
| [07-synchronization-rules.md](07-synchronization-rules.md) | The rules that define a correct synchronization. |
| [08-quality-and-acceptance.md](08-quality-and-acceptance.md) | Non-functional requirements and end-to-end acceptance scenarios. |

## The one-sentence summary

Build a command-line tool that keeps a person's files safely mirrored and de-duplicated across
several of their own cloud storage accounts (Google Drive, Microsoft OneDrive for Business, and
Telegram), using one primary account as the source of truth and the others as redundant,
load-balanced backup storage — without ever touching anything outside its own managed area.
