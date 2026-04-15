# mailbin

`mailbin` is a Go library for deleting IMAP email from a single account.

The package is intentionally library-only. It does not ship a CLI, account-file loader, or orchestration layer. Consumers are expected to implement their own process wiring, account sourcing, logging integration, and concurrency strategy.

## Table of Contents

- [Scope](#scope)
- [Install](#install)
- [Quick Start](#quick-start)
- [Public API](#public-api)
- [Provider to IMAP Address Resolution](#provider-to-imap-address-resolution)
- [Delete Semantics](#delete-semantics)
- [Partial Completion Handling](#partial-completion-handling)
- [Validation and Exported Errors](#validation-and-exported-errors)
- [Configuration Hooks](#configuration-hooks)
- [Retry and Attempt Tuning](#retry-and-attempt-tuning)
- [Tuning Example](#tuning-example)
- [Testing](#testing)
- [Consumer Design Guidance](#consumer-design-guidance)

## Scope

- Single-account IMAP delete client.
- Date cutoff delete criteria.
- Delete result metadata including partial completion information.
- Internal resilience/retry behavior for IMAP operations.

Out of scope:

- CLI commands.
- Built-in configuration file parsing.
- Built-in multi-account orchestration.
- Built-in background workers/schedulers.

## Install

```bash
go get github.com/lechefran/mailbin
```

## Quick Start

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
		Provider: "gmail",
		Email:    "user@example.com",
		Password: "app-password",
		Logf:     log.Printf, // optional
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := client.Delete(ctx, mailbin.DeleteCriteria{
		ReceivedBefore: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil && !errors.Is(err, mailbin.ErrDeleteIncomplete) {
		log.Fatal(err)
	}

	log.Printf("deleted=%d incomplete=%v", len(result.Deleted), result.Incomplete)
}
```

## Public API

Core entry points:

- `mailbin.NewClient(config Config) (*Client, error)`
- `(*Client).Delete(ctx context.Context, criteria DeleteCriteria) (DeleteResult, error)`
- `mailbin.ResolveIMAPAddress(provider, address string) (string, error)`

Main types:

- `Config`
- `DeleteCriteria`
- `DeleteResult`
- `MessageSummary`
- `DeleteIncompleteError`

## Provider to IMAP Address Resolution

You can provide either:

- `Config.Address` explicitly (`host:port`), or
- `Config.Provider` with built-in mapping.

If both are set, explicit `Address` wins.

Exported provider address constants:

- `mailbin.AOL`
- `mailbin.AOL_EXPORT`
- `mailbin.GMAIL`
- `mailbin.ICLOUD`
- `mailbin.OUTLOOK`
- `mailbin.YAHOO`
- `mailbin.ZOHO`

## Delete Semantics

Delete criteria:

```go
type DeleteCriteria struct {
    ReceivedBefore time.Time
}
```

Behavior:

- Deletes messages strictly older than `ReceivedBefore`.
- Rejects zero cutoff with `ErrReceivedBeforeRequired`.
- Returns deleted message summaries.
- Skips flagged/starred messages.

`DeleteResult` fields:

- `Deleted []MessageSummary`
- `Incomplete bool`
- `RemainingMatchingCount int`
- `RemainingMatchingCountKnown bool`
- `SkippedMailboxCount int`

## Partial Completion Handling

Some IMAP operations can produce partial outcomes. In that case:

- `Delete` may return a non-nil error.
- `errors.Is(err, mailbin.ErrDeleteIncomplete)` may be true.
- `DeleteResult` still contains useful progress data.

Recommended pattern:

```go
result, err := client.Delete(ctx, criteria)
switch {
case err == nil:
	// complete success
case errors.Is(err, mailbin.ErrDeleteIncomplete):
	// partial success: inspect result metadata
default:
	// hard failure
}
```

## Validation and Exported Errors

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

## Configuration Hooks

`Config` supports dependency injection for integration and testing:

- `Logf func(format string, args ...any)`
- `TLSConfig *tls.Config`
- `DialTLSContext func(context.Context, string, *tls.Config) (net.Conn, error)`
- `LookupIPAddrs func(context.Context, string) ([]net.IPAddr, error)`

Use these hooks when integrating with custom logging, networking, or DNS behavior.

## Retry and Attempt Tuning

`Config` exposes retry/pass and batching controls.

- `0` means "use the package default".
- Negative values are rejected by `NewClient` with `ErrNegativeConfigValue`.

Config fields:

- `DeletePasses`
- `DeleteMailboxAttempts`
- `DeleteCommandRetries`
- `DeleteStoreRetries`
- `DeleteExpungeRetries`
- `MailboxCommandRetries`
- `FetchRetryAttempts`
- `BatchSize`

Default constants:

- `mailbin.DefaultDeletePasses` (`3`)
- `mailbin.DefaultDeleteMailboxAttempts` (`5`)
- `mailbin.DefaultDeleteCommandRetries` (`5`)
- `mailbin.DefaultDeleteStoreRetries` (`5`)
- `mailbin.DefaultDeleteExpungeRetries` (`5`)
- `mailbin.DefaultMailboxCommandRetries` (`3`)
- `mailbin.DefaultFetchRetryAttempts` (`3`)
- `mailbin.DefaultBatchSize` (`20`)

## Tuning Example

```go
client, err := mailbin.NewClient(mailbin.Config{
	Provider: "gmail",
	Email:    "user@example.com",
	Password: "app-password",

	DeletePasses:          2,
	DeleteMailboxAttempts: 4,
	DeleteCommandRetries:  4,
	DeleteStoreRetries:    4,
	DeleteExpungeRetries:  4,
	MailboxCommandRetries: 2,
	FetchRetryAttempts:    2,
	BatchSize:             50,
})
```

Quick reference:

| Config Field | Default | Description |
| --- | --- | --- |
| `DeletePasses` | `3` | Full account-wide delete passes across mailboxes |
| `DeleteMailboxAttempts` | `5` | Attempts per mailbox delete flow |
| `DeleteCommandRetries` | `5` | Retries for mailbox `SELECT` and UID search |
| `DeleteStoreRetries` | `5` | Retries for UID `STORE`/`MOVE` actions |
| `DeleteExpungeRetries` | `5` | Retries for `EXPUNGE` |
| `MailboxCommandRetries` | `3` | Retries for mailbox `LIST`/`SELECT` commands |
| `FetchRetryAttempts` | `3` | Retries for UID `FETCH` summary calls |
| `BatchSize` | `20` | Shared batch size for fetch + delete processing |

Rules:

- Set a field to `0` to use the default.
- Negative values are invalid and return `ErrNegativeConfigValue` from `NewClient`.

## Testing

Run all tests:

```bash
go test ./...
```

## Consumer Design Guidance

To keep responsibilities clear, consumers should implement:

- account source (env, file, DB, secret manager),
- per-account timeout policy and retry policy at workflow level,
- multi-account orchestration (serial, worker pool, queue),
- job-level logging and observability,
- scheduling/triggering strategy.

`mailbin` stays focused on account-local delete behavior so you can build these policies in your own application architecture.
