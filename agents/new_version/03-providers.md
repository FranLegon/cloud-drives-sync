# 3. Providers and Their Invariants

The tool supports three providers. Each behaves differently, but each must uphold the same
high-level promise: the managed area presents one coherent, consistent library, and nothing outside
it is ever touched. This document states the per-provider behavior in terms of outcomes.

General rule for all providers: the tool authenticates as the user, acts only within the managed
area, retries transient failures gracefully, and streams large transfers rather than loading whole
files into memory.

## 3.1 Google Drive (hosts the main account)

- The main account is always a Google account. There is a single managed root that the main account
  owns.
- The same managed root is **shared** with every backup account so they can collaborate inside it.
  Backup accounts may own individual files within the structure, but the folder structure itself is
  owned by the main account.
- The tool can repair this arrangement: it must be able to detect when a backup account has lost the
  necessary access and re-grant it.
- When the tool needs to relocate a file's ownership between Google accounts (for capacity reasons),
  it should prefer the provider's native ownership-transfer capability. If that is unavailable,
  fails, or would require manual acceptance by the recipient, it must fall back to copying the file
  to the destination and removing the original — but only after the copy is confirmed intact.

**Invariant:** Within the main account's managed root, the folder structure is owned and maintained
by the main account; backup accounts contribute file content, not structure.

## 3.2 Microsoft OneDrive for Business (backup storage only)

- Each Microsoft backup account has its **own** managed root that it owns outright, including all
  sub-structure.
- File content is distributed across the Microsoft backup accounts. To still present a complete,
  identical folder tree in every Microsoft account, an account that does not physically hold a file
  must show a reference to it in the correct location (a native shortcut, or a placeholder when a
  native shortcut cannot be created).
- The complete folder structure — including empty folders and folders that exist only for hierarchy —
  must be mirrored across all Microsoft backup accounts. If a folder exists anywhere in the managed
  structure, it must exist at the corresponding path in each Microsoft backup account.
- Ownership of a folder follows ownership of the file it contains.
- Moving a file's storage between Microsoft accounts is done by copying to the destination and
  removing the source, never destroying the source before the copy is verified.

**Invariant:** Every Microsoft backup account presents the same full directory tree, composed of a
mix of files it owns, native shortcuts, and placeholders — never a missing entry and never a
redundant real copy.

## 3.3 Telegram (backup storage only)

- Telegram has no real folder hierarchy, so the tool cannot rely on the provider to remember
  structure. All structural and identity information for a file stored on Telegram must be carried
  **with** the stored object itself, in addition to being tracked in the tool's own state, so that a
  file's logical path, identity, and fragment ordering can always be recovered.
- Files are kept in a single dedicated managed destination on Telegram. As with other providers, if
  more than one candidate destination with the managed name exists, the tool stops and asks the user
  to disambiguate.
- Because a stored object's final provider identifier is only known after it is stored, the tool may
  need to store the object first and then attach its complete identifying information once the
  identifier is known. The end state must be that every stored object carries complete, correct
  identifying information.
- Files larger than Telegram's per-object size limit are stored as ordered fragments, each tracked
  so the original can be reassembled exactly.
- Incidental, non-file system messages in the destination (notices about creation, membership
  changes, and similar) are not data and must be ignored.

**Invariant:** Everything the tool stores on Telegram is self-describing enough to fully reconstruct
the original file, its identity, and its place in the logical tree, even if the tool's local state
were lost.

## 3.4 Why content cannot simply be hash-compared across providers

Different providers fingerprint content differently, so a replica on one provider and a replica on
another cannot be matched purely by comparing provider fingerprints. The tool must therefore decide
"these are the same logical file" using path plus the lightweight name/size identity, and use
provider fingerprints only to compare two replicas **on the same provider**. Keep this limitation in
mind wherever the rules talk about detecting duplicates or detecting conflicts.
