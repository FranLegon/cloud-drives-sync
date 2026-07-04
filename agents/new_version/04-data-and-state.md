# 4. Data and State the Tool Must Track

The tool needs a durable, local record of what it knows about the user's pool. This document
describes **what information** must be tracked and **why**, not how to store it. You may choose any
storage technology and any schema, provided it can answer every question listed here efficiently and
survive being read back after a crash.

The local state must be encrypted at rest (see [05-security-and-configuration.md](05-security-and-configuration.md))
and must be queryable by the user outside the tool using their master password.

## 4.1 What the state is for

The state is the tool's memory between runs. With it, the tool can:

- Decide what already exists where, without re-scanning everything from scratch.
- Detect that a file present on one provider is missing on another.
- Detect duplicates within a provider.
- Detect that a replica that used to exist has disappeared (data loss) and trigger restoration.
- Reassemble fragmented files in the correct order.
- Reconstruct the logical folder tree even on providers that do not store one.

## 4.2 Logical files

For each logical file the tool is managing, it must be able to record and retrieve:

- A stable internal identity that does not change as the file moves between accounts.
- The file's logical location in the tree (its path) and its name.
- Its size.
- A lightweight identity derived from name and size, used to recognize the same file across
  providers and to group potential duplicates.
- A last-modified time.
- Its lifecycle state: active, soft-deleted, or permanently deleted.

## 4.3 Replicas (physical copies)

For each physical copy of a logical file on a specific account, the tool must be able to record and
retrieve:

- A link back to the logical file it is a copy of.
- Which provider and which account holds it.
- The provider's own stable identifier for this copy (so the tool can find, move, or delete it
  later, and detect when it has moved).
- The provider's content fingerprint for this copy, where the provider supplies one.
- Its location, name, and size as seen on that provider.
- A last-modified time.
- Its lifecycle state.
- Whether it is stored as fragments.
- Who owns the copy (which may differ from the account it is visible in, e.g. a shared file).
- When it was last confirmed to still exist. This timestamp is what lets the tool notice that a
  replica seen in the past is now gone, which signals data loss and the need to restore from another
  replica.

The combination of provider, account, and the provider's identifier must uniquely identify a
replica; the tool must not create two records for the same physical copy.

## 4.4 Fragments

For a replica that is stored in pieces, the tool must track, for each piece:

- Which replica it belongs to.
- Its position in the sequence and the total number of pieces.
- Its size.
- The provider identifier needed to retrieve that specific piece.

This must be sufficient to download every piece and concatenate them, in order, into a byte-exact
copy of the original file.

## 4.5 Folders

The tool must track the folder structure so it can recreate it on providers that do not natively
preserve structure and verify it on those that do. For each folder it must know:

- Its identity and name.
- Its logical path and its parent.
- Which provider and account it belongs to, and who owns it.

## 4.6 Consistency expectations

- Writes to the local state must be serialized so the state never becomes internally inconsistent,
  even when the tool is talking to many accounts at once.
- The state is an optimization and a record, not the ultimate authority: the cloud accounts are.
  When the state and reality disagree, the tool reconciles by trusting what it observes in the
  accounts and updating its state accordingly — while never destroying data on the basis of stale
  state alone.
- The state must be portable: it can be lost and rebuilt by re-scanning the accounts, and (for
  Telegram especially) the information needed to rebuild it travels with the stored files.

## 4.7 Sharing the state across the pool

So that the tool can run from any machine and so the state itself is redundant, the authoritative
local state is itself treated as a managed file: after any operation that changes it, the updated
state is uploaded into the auxiliary area of the main account and the backup accounts. Before an
operation that needs the state, the tool obtains the most appropriate available copy, preferring a
local copy, then the main account, then the other providers in a defined order. If no copy exists
anywhere and the operation is not the very first-time setup, the tool stops with a clear error
rather than proceeding blind.
