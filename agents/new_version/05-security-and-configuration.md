# 5. Security and Configuration

This document states the trust model and the configuration goals as outcomes. It does not mandate
specific algorithms, file formats, or libraries — choose strong, well-established ones that satisfy
these requirements.

## 5.1 Trust model

- The only secret the user must remember is a single **master password**, supplied at runtime on
  every execution.
- Everything sensitive the tool persists — provider credentials, authentication tokens/sessions, and
  the tool's tracked state — must be unreadable to anyone who does not have the master password.
- Nothing sensitive is ever written to disk in cleartext, and nothing sensitive is printed to logs.
- A leaked copy of the tool's files, without the master password, must not reveal credentials,
  tokens, or file contents.

## 5.2 Encryption requirements

- All persisted secrets and all persisted state must be **encrypted at rest** using strong,
  modern, authenticated encryption.
- The encryption key must be **derived from the master password** using a slow, salt-based,
  memory-hard key-derivation approach suitable for resisting brute-force attacks.
- A unique random salt must be generated once on first setup and reused for all later key
  derivations, persisted alongside the encrypted data.
- The same master password must unlock both the configuration and the tracked state, and the
  tracked state must remain queryable by the user with standard tooling using that password — the
  tool must not be the only thing that can open it.

## 5.3 Configuration content

The configuration captures everything needed to operate the pool without re-asking the user:

- The application-level credentials the tool needs to talk to each provider's API.
- The set of configured accounts, each with: its provider, its identifier (email or phone), whether
  it is the main account, and the durable credential (refresh token or session) needed to act on its
  behalf later without re-prompting.

Exactly one account is the main account. The configuration must make the main account unambiguous.

## 5.4 Configuration lifecycle

- On first-time setup, the tool collects the provider application credentials and the master
  password, establishes the salt, authenticates the main account, and persists everything encrypted.
- The tool can later add backup accounts, remove backup accounts, and update credentials, always
  keeping the encrypted configuration consistent.
- Removing accounts only changes local configuration; it must never delete the user's data in the
  cloud as a side effect.
- A main account may only be removed once it has no backup accounts depending on it for its provider.

## 5.5 Authentication and re-authentication

- Adding an account walks the user through that provider's standard authorization flow and stores
  the resulting durable credential.
- The tool can check, on demand, whether each stored credential still works, and report exactly
  which accounts need attention.
- The tool can re-authenticate accounts whose credentials have expired or been revoked. By default
  it only re-authenticates the ones that are actually broken; on request it can re-authenticate all.
- Re-authentication must verify that the re-authenticated identity matches the original account, so
  a user cannot accidentally swap one account for a different one.

## 5.6 Non-interactive operation

- The master password can be supplied non-interactively so the tool can run unattended.
- When secrets are required and not supplied, the tool prompts; but it must always be possible to
  run completely unattended by supplying everything up front.
- The tool must never echo secrets and must never require a secret to be passed in a way that leaks
  it into logs or process listings unnecessarily.

## 5.7 Self-contained deployment mode

There must be a way to produce a **self-contained deployment** of the tool that already carries its
encrypted configuration with it, so it can run on a schedule on another machine with no setup step —
still requiring the master password at runtime to unlock. In this mode the tool only needs to expose
the capabilities required for unattended operation (running the full sync workflow and managing its
own schedule), and may refuse setup/maintenance capabilities that do not make sense without an
interactive operator.
