# 7. Synchronization Rules

This is the heart of the tool. It defines what a **correct** synchronization is. The rules are
stated as the end state the tool must converge toward, not as an algorithm. Whatever approach you
take, the observable result after a successful sync must satisfy every rule here.

## 7.1 The goal state

After a successful synchronization, all of the following are true:

1. Every **active** logical file exists, with identical content, on the providers it should exist
   on, in the same logical location, with the same name.
2. The **folder structure** is identical across all participating providers (excluding Telegram,
   which has no structure), including empty and hierarchy-only folders.
3. No provider is missing an active file that another provider has, unless that file is
   soft-deleted.
4. No file that has been soft-deleted remains active anywhere.
5. No spurious duplicate copies were created by the sync itself.
6. Files that had been lost from one provider but survive on another have been restored.

## 7.2 Matching files across providers

- Two files are considered "the same logical file" when they share the same logical path and the
  same lightweight name+size identity. This is how the tool recognizes correspondence across
  providers that do not share a comparable content fingerprint.
- Within a single provider, byte-level sameness (and therefore duplication and content conflicts) is
  judged using that provider's own content fingerprint.

## 7.3 Filling gaps

- If an active logical file exists on at least one provider but is absent on a provider where it
  should exist, the tool transfers it there, recreating any folders needed to place it correctly.
- Transfers must stream content and must tolerate large files, using fragmentation where a provider
  requires it.

## 7.4 Conflicts

- A conflict exists when the same logical location and name is occupied by **different content** on
  different sides. The tool must never silently overwrite one with the other and never merge them.
- The tool resolves a conflict by keeping both: the incoming/divergent copy is preserved under a
  clearly distinguished, timestamped name so no version is lost, and the user can reconcile later.

## 7.5 Soft deletion and restoration

- A file that has been moved into the soft-deleted area is **not** re-pushed to providers that lack
  it. Its absence elsewhere is intentional, not a gap to fill.
- If the same file is found soft-deleted on one provider but still active on another, the active
  copies are brought into the soft-deleted state so the deletion is consistent everywhere.
- If a soft-deleted file is moved back into the active tree, normal sync resumes for it and it is
  re-mirrored to all providers.

## 7.6 Hard deletion

- Permanent deletion only ever acts on files that are already soft-deleted.
- When triggered, it removes every replica of that file from every account and clears it from the
  tracked state. This is the only path by which the tool permanently destroys data, and it must be
  deliberate.

## 7.7 Loss detection and self-healing

- The tool distinguishes "the user soft-deleted this" from "this copy vanished unexpectedly." A
  replica that was previously recorded and is no longer observed, without having been soft-deleted,
  is treated as **lost**.
- When a logical file has lost a replica on one provider but still has a healthy replica on another,
  the tool restores the missing replica from a surviving one — including reassembling it from
  fragments when the surviving copy is fragmented (e.g., restoring a large file to Google and
  Microsoft from the fragments preserved on Telegram).

## 7.8 Consistency of structure on a fragmented/structureless provider

- On a provider with no real folders, the logical path and structure of every file must still be
  preserved through the information stored with the file, so that the same logical tree can be
  presented and so files can be restored to the correct location elsewhere.

## 7.9 Idempotence and convergence

- Running synchronization when everything is already consistent must make no changes.
- Running synchronization after an interruption must complete the remaining work without duplicating
  the work already done and without creating duplicate copies.
- Synchronization must converge: repeated runs move the pool toward the goal state and, once there,
  keep it there.

## 7.10 Ordering within the full workflow

When capabilities are combined into the full workflow, they run in an order that is internally
consistent: assess capacity, reduce what the main account holds, eliminate duplicates, make
providers consistent, then rebalance remaining storage pressure. Each step leaves the pool in a
valid state for the next, and the whole sequence is safe to re-run.
