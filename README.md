# mailbin

`mailbin` is a Go library for deleting IMAP email from a single account using an age/cutoff-based rule.

The repository also ships a CLI (`cmd/mailbin`) that acts as a consumer of the library. The library keeps the API focused on one-account delete operations, while the CLI handles multi-account orchestration, config loading, prompts, and process-level concerns.

## Table of Contents

- [Status and Scope](#status-and-scope)
- [Repository Layout](#repository-layout)
- [Install](#install)
- [Quick Start (Library)](#quick-start-library)
- [Core Delete Semantics](#core-delete-semantics)
- [Public API](#public-api)
- [Provider Resolution](#provider-resolution)
- [Delete Results and Partial Completion](#delete-results-and-partial-completion)
- [Logging and Dependency Injection](#logging-and-dependency-injection)
- [Using Context and Timeouts](#using-context-and-timeouts)
- [CLI Usage](#cli-usage)
- [Configuration File Format](#configuration-file-format)
- [CLI Output Format](#cli-output-format)
- [Error Handling Guide](#error-handling-guide)
- [Testing](#testing)
- [Security Notes](#security-notes)
- [Design Notes for Consumers](#design-notes-for-consumers)

## Status and Scope

This project is intentionally scoped as a **delete-focused** IMAP toolset.

- The `mailbin` library exposes a single-account delete client.
- The library does not provide built-in multi-account orchestration APIs.
- The bundled CLI supports multi-account execution as a consumer concern.
- Read/search mailbox workflows are internal implementation details to support delete execution and verification.

## Repository Layout

```text
.
├── client.go                # Public library API (package mailbin)
├── doc.go                   # Package documentation
├── cmd/
│   └── mailbin/
│       ├── main.go          # CLI entrypoint
│       └── app.go           # CLI flag parsing + orchestration/output
├── internal/
│   ├── imap/                # IMAP implementation details
│   └── cliconfig/           # CLI account/config loading helpers
└── accounts.example.json    # Example multi-account CLI config
```

## Install

Add the module to your Go project:

```bash
go get github.com/lechefran/mailbin
```

The module currently targets the Go version declared in `go.mod`.

## Quick Start (Library)

```go
package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/lechefran/mailbin"
)

func main() {
	client, err := mailbin.NewClient(mailbin.Config{
		Provider: "gmail",          // or set Address directly
		Email:    "user@example.com",
		Password: "app-password",
		Logf:     log.Printf,       // optional
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cutoff := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	result, err := client.Delete(ctx, mailbin.DeleteCriteria{
		ReceivedBefore: cutoff,
	})

	if err != nil && !errors.Is(err, mailbin.ErrDeleteIncomplete) {
		log.Fatal(err)
	}

	log.Printf("deleted=%d incomplete=%v", len(result.Deleted), result.Incomplete)
}
```

## Core Delete Semantics

The delete rule is driven by:

```go
type DeleteCriteria struct {
    ReceivedBefore time.Time
}
```

Behavior:

- Messages are deleted when they are strictly **before** `ReceivedBefore`.
- A zero `ReceivedBefore` is rejected with `ErrReceivedBeforeRequired`.
- Flagged/starred messages are skipped by delete logic.
- The operation can return partial progress metadata when completion cannot be fully verified.

## Public API

### Constructors and Entry Points

- `ResolveIMAPAddress(provider, address string) (string, error)`
- `NewClient(config Config) (*Client, error)`
- `(*Client).Delete(ctx context.Context, criteria DeleteCriteria) (DeleteResult, error)`

### `Config`

`mailbin.Config` configures one account:

- `Provider string`
- `Address string` (IMAP server as `host:port`)
- `Email string`
- `Password string`
- `Logf func(format string, args ...any)` (optional logger injection)
- `TLSConfig *tls.Config` (optional)
- `DialTLSContext func(context.Context, string, *tls.Config) (net.Conn, error)` (optional dial injection)
- `LookupIPAddrs func(context.Context, string) ([]net.IPAddr, error)` (optional DNS lookup injection)

Use either:

- `Address` directly, or
- `Provider` with built-in resolution.

If both are set, explicit `Address` is used.

### `MessageSummary`

Each deleted message is reported as:

- `Mailbox`
- `MessageID`
- `SequenceNumber`
- `UID`
- `Flagged`
- `ReceivedAt`
- `Subject`
- `From`
- `To`

### `DeleteResult`

`DeleteResult` includes:

- `Deleted []MessageSummary`
- `Incomplete bool`
- `RemainingMatchingCount int`
- `RemainingMatchingCountKnown bool`
- `SkippedMailboxCount int`

`Incomplete` may be `true` even when `Deleted` contains successful deletions.

## Provider Resolution

`ResolveIMAPAddress` supports built-in providers and aliases.

Primary exported constants:

- `mailbin.AOL`
- `mailbin.AOL_EXPORT`
- `mailbin.GMAIL`
- `mailbin.ICLOUD`
- `mailbin.OUTLOOK`
- `mailbin.YAHOO`
- `mailbin.ZOHO`

Built-in provider aliases:

- `aol`, `aol-export`, `aolexport`
- `gmail`, `googlemail`
- `hotmail`, `live`, `outlook`, `office365`, `microsoft365`
- `icloud`
- `yahoo`
- `zoho`

If neither provider nor address is supplied, you get `ErrIMAPAddressOrProviderRequired`.
If provider is unknown, you get `ErrUnsupportedProvider`.

## Delete Results and Partial Completion

Delete may succeed fully, fail fully, or finish partially.

Partial completion is represented by:

- `errors.Is(err, mailbin.ErrDeleteIncomplete) == true`
- Result metadata (`Incomplete`, `RemainingMatchingCount`, `SkippedMailboxCount`)
- Optional typed error details via `*mailbin.DeleteIncompleteError`

Pattern:

```go
result, err := client.Delete(ctx, criteria)
if err != nil {
    if errors.Is(err, mailbin.ErrDeleteIncomplete) {
        // inspect result + typed error for partial progress
    } else {
        // hard failure
    }
}
```

This is useful for batch jobs where partial progress should be recorded and retried later.

## Logging and Dependency Injection

The library intentionally accepts injected hooks instead of hardcoding global behavior.

- `Config.Logf`: inject your logger (`log.Printf`, structured logger adapter, no-op logger)
- `Config.TLSConfig`: customize TLS
- `Config.DialTLSContext`: override network dialing behavior
- `Config.LookupIPAddrs`: override host lookup behavior

This keeps integration flexible for services, workers, and tests.

## Using Context and Timeouts

All delete operations are context-driven:

- Pass request/job scoped contexts.
- Use `context.WithTimeout` or `context.WithDeadline` from the caller.
- Treat cancellation/deadline exceed as normal operational outcomes in automation.

Example:

```go
ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
defer cancel()

result, err := client.Delete(ctx, criteria)
```

## CLI Usage

Entry point from repository root:

```bash
go run ./cmd/mailbin ...
```

Show flags:

```bash
go run ./cmd/mailbin -h
```

### Run with a config file

```bash
go run ./cmd/mailbin -config ./accounts.json -age 30
```

Run one named account from config:

```bash
go run ./cmd/mailbin -config ./accounts.json -account gmail -age 30
```

### Run a single account without config file

```bash
MAILBIN_PASSWORD='your-app-password' \
go run ./cmd/mailbin \
  -provider gmail \
  -email you@gmail.com \
  -age 30
```

You can also pass `-imap-addr` to bypass provider mapping:

```bash
MAILBIN_PASSWORD='your-app-password' \
go run ./cmd/mailbin \
  -imap-addr imap.example.com:993 \
  -email you@example.com \
  -age 30
```

### CLI Flags

- `-config string` path to accounts config JSON
- `-account string` account name from config to run
- `-provider string` provider for built-in IMAP defaults
- `-imap-addr string` explicit IMAP address (`host:port`)
- `-email string` email for login
- `-age int` minimum email age in days to delete (**required**, must be `>= 0`)
- `-timeout duration` per-account connection timeout (default `30s`)

### CLI Environment Variables

Environment-backed defaults:

- `MAILBIN_CONFIG`
- `MAILBIN_ACCOUNT`
- `MAILBIN_PROVIDER`
- `MAILBIN_IMAP_ADDR`
- `MAILBIN_EMAIL`
- `MAILBIN_AGE`
- `MAILBIN_PASSWORD` (used when no config file and interactive prompt should be avoided)

## Configuration File Format

Use `accounts.example.json` as reference:

```json
{
  "accounts": [
    {
      "name": "gmail",
      "email": "you@gmail.com",
      "provider": "gmail",
      "password_env": "MAILBIN_GMAIL_PASSWORD"
    },
    {
      "name": "icloud",
      "email": "you@icloud.com",
      "provider": "icloud",
      "password_env": "MAILBIN_ICLOUD_PASSWORD"
    },
    {
      "name": "outlook",
      "email": "you@example.com",
      "imap_addr": "imap.example.com:993",
      "password_env": "MAILBIN_CUSTOM_PASSWORD"
    }
  ]
}
```

Per account fields:

- `name` optional logical account name
- `provider` optional provider key (if `imap_addr` not set)
- `imap_addr` optional explicit address override
- `email` required for login
- `password_env` optional environment variable key holding password

Password resolution behavior:

- If `password_env` is set and present, its value is used.
- If missing and stdin is interactive, CLI prompts for password.
- If missing and stdin is non-interactive, CLI returns an error.

## CLI Output Format

For each deleted message:

```text
<received_at> | [account=<name> | ]mailbox=<mailbox> | <subject> | from=<from> | to=<to> | uid=<uid>
```

Then summary lines:

```text
deleted <N> emails
summary: deleted total=<N> emails across accounts=<A> (successful=<S> failed=<F>)
```

## Error Handling Guide

Common exported errors:

- `ErrIMAPAddressOrProviderRequired`
- `ErrUnsupportedProvider`
- `ErrClientRequired`
- `ErrIMAPAddressRequired`
- `ErrEmailRequired`
- `ErrPasswordRequired`
- `ErrReceivedBeforeRequired`
- `ErrLoginFailed`
- `ErrDeleteIncomplete`

Recommended pattern:

```go
result, err := client.Delete(ctx, criteria)
switch {
case err == nil:
    // complete success
case errors.Is(err, mailbin.ErrDeleteIncomplete):
    // partial success; use result metadata
default:
    // hard failure
}
```

## Testing

Run all tests:

```bash
go test ./...
```

Run only library package tests:

```bash
go test ./...
go test . -run Test
```

## Security Notes

- Use app passwords where providers require them.
- Avoid committing credentials or secret-bearing config files.
- Prefer environment variables or secret managers for passwords.
- Be careful with log sinks if you inject verbose logging in production workflows.

## Design Notes for Consumers

The library is designed to be embedded into your own job runner or service.

- Keep account loading and secret resolution outside the library.
- Implement orchestration strategy in your application (serial, worker pool, queue-driven).
- Decide policy for partial completion (`ErrDeleteIncomplete`) and retries.
- Inject your own logger and network hooks using `Config`.

If you want the same behavior as this repository’s CLI, use `cmd/mailbin` as a reference consumer implementation.
