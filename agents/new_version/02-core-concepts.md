# 2. Core Concepts and Vocabulary

This document defines the mental model. Every other document assumes these terms.

## 2.1 Accounts and roles

- **Provider** — A cloud storage service the tool can talk to. There are three: Google Drive,
  Microsoft OneDrive for Business, and Telegram.
- **Account** — One of the user's individual logins on a provider (an email address for Google and
  Microsoft, a phone number for Telegram). The user owns several.
- **Main account** — Exactly one account is designated the primary. It is the source of truth for
  the folder structure and the account the tool actively works to keep empty. The main account is
  always on Google Drive.
- **Backup account** — Every other configured account. Backup accounts provide redundant storage
  capacity and live data copies. There can be many, across any provider.

The user configures one main account and any number of backup accounts.

## 2.2 The managed area ("the fence")

The tool is only ever allowed to operate inside a clearly delimited managed area within each
account. Conceptually:

- There is **one managed root area** per relevant account, with a well-known, stable name.
- Inside that root there is a normal tree of folders and files that the tool keeps in sync.
- Inside that root there is also a reserved **auxiliary area** used by the tool itself for its own
  housekeeping (for example, holding soft-deleted items and storing its synchronized state). The
  auxiliary area is part of the fence but is treated differently from ordinary user content.

Anything outside the managed root — the rest of the user's drive — is out of bounds. The tool must
never enumerate, read, alter, or delete it.

**Pre-flight requirement:** Before doing any cloud work, the tool must confirm the fence is
well-formed for each account it will touch: exactly one managed root must exist and be reachable. If
zero exist it must create one where appropriate; if more than one exists the tool must stop and ask
the user to resolve the ambiguity rather than guess. Items the provider considers trashed/recycled
do not count as existing.

## 2.3 Logical files vs. physical replicas

This distinction is central to the whole design.

- A **logical file** is the abstract idea of "a file the user has," identified by where it lives in
  the folder tree and what it is. It is provider-agnostic. It has a lifecycle state: present and
  active, soft-deleted (hidden but recoverable), or permanently gone.
- A **replica** is one concrete physical copy of a logical file living in one specific account on
  one specific provider. A single logical file may have many replicas spread across accounts and
  providers. Each replica carries the provider's own stable identifier and content fingerprint for
  that copy.

The tool's job, restated in these terms, is to manage the relationship between logical files and
their replicas: ensure enough replicas exist in the right places, remove redundant ones, and keep
every replica's apparent location and identity consistent with its logical file.

## 2.4 File identity and equality

The tool must be able to answer two questions reliably:

1. **"Are these two replicas the same content?"** — Answered using a content fingerprint the
   provider supplies for the copy. Two replicas with the same fingerprint hold identical bytes.
2. **"Are these two replicas meant to be the same logical file?"** — Answered by a combination of
   where the file sits in the tree and a lightweight identity derived from its name and size. This
   is what lets the tool recognize that "the same file" exists across providers even when their
   content fingerprints are computed differently and cannot be compared directly.

Both notions of identity are required because providers do not share a common hashing scheme, and
because the same content can legitimately exist at different paths.

## 2.5 Soft deletion

Deleting a file is a two-stage, reversible-by-default process:

- **Soft delete** moves a file into the reserved soft-deleted part of the auxiliary area. A
  soft-deleted file is excluded from ordinary synchronization (it will not be re-pushed to accounts
  that lack it), but its soft-deleted state **is** propagated: if the same file still sits in an
  active location on another account, that copy is brought into the soft-deleted state too, so the
  deletion is honored everywhere.
- **Restore** moves a soft-deleted file back into the active tree, after which normal sync resumes
  and it is re-mirrored everywhere.
- **Hard delete** permanently removes a file that is already soft-deleted, from every account and
  from the tool's tracked state. This is irreversible and only acts on already-soft-deleted items.

## 2.6 Fragmentation

Some providers cap the size of a single stored object. A logical file larger than a provider's cap
must be stored on that provider as an ordered set of **fragments** that together reconstitute the
original bytes. The tool must track the fragments well enough to reassemble the original file
exactly, in order, on demand. To the rest of the system, a fragmented replica still represents one
whole logical file.

## 2.7 Shortcuts and placeholders

To present a consistent folder tree on a provider where files are spread across separate accounts,
an account that does not physically hold a given file may instead hold a **reference** to it in the
correct location:

- A **native shortcut** when the provider supports pointing at another account's file.
- A lightweight **placeholder** standing in for the file when a native shortcut cannot be created.
  A placeholder carries enough information (identity and size) to be recognized as "this file
  belongs here, the real bytes live elsewhere" so the tool does not mistakenly treat the location as
  empty and create a redundant copy.

Both are treated by the tool as valid evidence that the logical file is accounted for at that
location.
