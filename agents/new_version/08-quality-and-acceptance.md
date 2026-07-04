# 8. Quality Attributes and Acceptance

This document defines the non-functional bar the rebuild must clear and the end-to-end scenarios
that prove it works. The scenarios are written as observable outcomes; the self-test capability
(see [06-commands.md](06-commands.md)) should automate them against real accounts.

## 8.1 Non-functional requirements

- **Reliability.** Every cloud interaction tolerates transient failures (network blips, throttling)
  by backing off and retrying, and a whole run can be interrupted and safely resumed.
- **Data safety.** No operation destroys data before its replacement or relocation is verified.
  Destructive actions are deliberate, limited to the managed area, and reversible by default (soft
  delete) except for the explicit permanent-deletion path.
- **Isolation.** Under no circumstances does the tool read, alter, or delete anything outside the
  managed area. This must hold even when the tracked state is wrong or stale.
- **Efficiency.** Large files are streamed, not buffered whole. Independent read-only work across
  accounts may proceed in parallel; anything that mutates the shared tracked state is serialized to
  avoid corruption.
- **Scriptability.** Every capability can run unattended with all inputs supplied up front, and
  signals success or failure unambiguously to its caller.
- **Observability.** Meaningful actions and errors are logged with clear attribution to the
  provider and account involved, enough for a user to reconstruct what happened, without leaking
  secrets.
- **Portability of state.** The tracked state can be lost and fully rebuilt by re-scanning the
  accounts; for the structureless provider, the information needed to rebuild travels with the
  stored files.
- **Correctness over speed.** When in doubt, the tool favors the safe, consistent outcome over the
  fast one.
- **No dead ends.** The delivered product is complete: no unfinished capability, no placeholder that
  pretends to work but does not.

## 8.2 End-to-end acceptance scenarios

Each scenario starts from a known state, performs actions, then asserts the resulting state across
the relevant providers and the tracked state. A build is acceptable only when all pass.

1. **Clean slate.** From an empty managed area, setup and an initial scan produce an empty,
   well-formed pool with the fence correctly established on every account.
2. **One file from the main account.** A file placed on the main account is moved off it into
   backups and then mirrored, ending active and consistent everywhere it should be, with the main
   account emptied of it.
3. **Files originating on different backup providers.** Files created directly on backup accounts of
   different providers are synchronized so that each becomes consistently present across the pool.
4. **A moderately large file.** A file large enough to exercise streaming is uploaded and
   synchronized intact across providers.
5. **Moves of files and folders.** Creating folders and moving files into them propagates the new
   structure and locations to all providers, with no duplicates left behind.
6. **Soft deletion.** Moving files into the soft-deleted area causes them to leave the active set
   everywhere and be marked soft-deleted consistently, without being re-pushed.
7. **Nested folders.** Deeply nested folder structures are created and mirrored faithfully across
   providers.
8. **Restore from soft-deleted.** Moving a soft-deleted file back to the active tree restores it to
   active and re-mirrors it everywhere.
9. **Permanent deletion.** Hard-deleting already-soft-deleted files removes them from every provider
   and from the tracked state permanently.
10. **Quota consistency.** Reported usage and availability per provider are coherent and the
    cross-provider sanity check behaves correctly.
11. **A file requiring fragmentation.** A file larger than a provider's per-object limit is stored
    as ordered fragments on that provider and remains fully reconstructable.
12. **Self-healing from loss.** After a file is removed directly from some providers (simulating
    data loss) and its corresponding tracked replicas are dropped, a synchronization restores the
    file to those providers using a surviving replica elsewhere — including reassembling it from
    fragments when the survivor is fragmented.
13. **Ownership relocation.** Relocating a file's ownership/storage between accounts behaves
    correctly, using native transfer when possible and the safe copy-then-remove fallback otherwise,
    never losing the file.
14. **Duplicate handling.** Byte-identical copies within a provider are detected and removed by both
    the interactive and the automatic paths, leaving exactly one surviving copy as specified.
15. **Conflict handling.** Divergent content at the same logical location is preserved as both
    versions, with the divergent one clearly distinguished, never silently overwritten.
16. **Idempotence.** Running synchronization again immediately after a successful run makes no
    changes anywhere.
17. **Resumability.** A synchronization interrupted partway and re-run completes the remaining work
    without creating duplicates or losing data.

## 8.3 Definition of done

The rebuild is done when:

- Every capability in [06-commands.md](06-commands.md) is fully implemented and reachable.
- Every synchronization rule in [07-synchronization-rules.md](07-synchronization-rules.md) holds.
- Every acceptance scenario in [8.2](#82-end-to-end-acceptance-scenarios) passes against real
  accounts, unattended.
- Every non-functional requirement in [8.1](#81-non-functional-requirements) is satisfied.
- There are no unfinished pieces, stubs, or known correctness bugs in the managed behaviors.
