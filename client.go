package mailbin

import (
	"context"
	"time"

	internalimap "github.com/lechefran/mailbin/internal/imap"
)

// Config configures a single-account delete client.
type Config = internalimap.Config

// DeleteResult reports deleted messages and any incomplete-delete metadata.
type DeleteResult = internalimap.DeleteResult

// DeleteIncompleteError reports that a delete completed only partially.
type DeleteIncompleteError = internalimap.DeleteIncompleteError

// DeleteCriteria defines which messages the client should delete.
type DeleteCriteria struct {
	// ReceivedBefore deletes messages strictly before this timestamp.
	ReceivedBefore time.Time
	// FromAccounts deletes messages from these sender email accounts regardless of age.
	FromAccounts []string
}

// MessageSummary describes a deleted message.
type MessageSummary = internalimap.MessageSummary

var (
	// ErrIMAPAddressOrProviderRequired indicates that neither a provider nor an IMAP address was supplied.
	ErrIMAPAddressOrProviderRequired = internalimap.ErrIMAPAddressOrProviderRequired
	// ErrUnsupportedProvider indicates that the requested provider does not have a built-in IMAP address mapping.
	ErrUnsupportedProvider = internalimap.ErrUnsupportedProvider
	// ErrClientRequired indicates that a nil client was used.
	ErrClientRequired = internalimap.ErrClientRequired
	// ErrIMAPAddressRequired indicates that the client configuration is missing an IMAP address.
	ErrIMAPAddressRequired = internalimap.ErrIMAPAddressRequired
	// ErrEmailRequired indicates that the client configuration is missing an email address.
	ErrEmailRequired = internalimap.ErrEmailRequired
	// ErrPasswordRequired indicates that the client configuration is missing a password.
	ErrPasswordRequired = internalimap.ErrPasswordRequired
	// ErrDeleteCriteriaRequired indicates that delete criteria did not include any supported condition.
	ErrDeleteCriteriaRequired = internalimap.ErrDeleteCriteriaRequired
	// ErrReceivedBeforeRequired indicates that a delete-before operation did not include a cutoff time.
	ErrReceivedBeforeRequired = internalimap.ErrReceivedBeforeRequired
	// ErrLoginFailed indicates that IMAP authentication failed.
	ErrLoginFailed = internalimap.ErrLoginFailed
	// ErrDeleteIncomplete indicates that a delete finished with partial progress or incomplete verification.
	ErrDeleteIncomplete = internalimap.ErrDeleteIncomplete
	// ErrNegativeConfigValue indicates that a configurable numeric value was negative.
	ErrNegativeConfigValue = internalimap.ErrNegativeConfigValue
)

const (
	// AOL is the built-in IMAP endpoint for AOL Mail.
	AOL = internalimap.AOL
	// AOL_EXPORT is the built-in IMAP endpoint for AOL export accounts.
	AOL_EXPORT = internalimap.AOL_EXPORT
	// GMAIL is the built-in IMAP endpoint for Gmail.
	GMAIL = internalimap.GMAIL
	// ICLOUD is the built-in IMAP endpoint for iCloud Mail.
	ICLOUD = internalimap.ICLOUD
	// OUTLOOK is the built-in IMAP endpoint for Outlook and Microsoft 365 mailboxes.
	OUTLOOK = internalimap.OUTLOOK
	// YAHOO is the built-in IMAP endpoint for Yahoo Mail.
	YAHOO = internalimap.YAHOO
	// ZOHO is the built-in IMAP endpoint for Zoho Mail.
	ZOHO = internalimap.ZOHO
)

const (
	// DefaultDeletePasses is the default number of full mailbox delete passes.
	DefaultDeletePasses = internalimap.DefaultDeletePasses
	// DefaultDeleteMailboxAttempts is the default number of attempts per mailbox delete flow.
	DefaultDeleteMailboxAttempts = internalimap.DefaultDeleteMailboxAttempts
	// DefaultDeleteCommandRetries is the default retry count for mailbox-select and search commands during delete.
	DefaultDeleteCommandRetries = internalimap.DefaultDeleteCommandRetries
	// DefaultDeleteStoreRetries is the default retry count for UID STORE and UID MOVE commands.
	DefaultDeleteStoreRetries = internalimap.DefaultDeleteStoreRetries
	// DefaultDeleteExpungeRetries is the default retry count for EXPUNGE commands.
	DefaultDeleteExpungeRetries = internalimap.DefaultDeleteExpungeRetries
	// DefaultMailboxCommandRetries is the default retry count for mailbox LIST/SELECT operations.
	DefaultMailboxCommandRetries = internalimap.DefaultMailboxCommandRetries
	// DefaultFetchRetryAttempts is the default retry count for FETCH summary batches.
	DefaultFetchRetryAttempts = internalimap.DefaultFetchRetryAttempts
	// DefaultBatchSize is the default batch size for fetch and delete processing.
	DefaultBatchSize = internalimap.DefaultBatchSize
)

// Client deletes messages for a single account.
type Client struct {
	imap *internalimap.Client
}

// ResolveIMAPAddress resolves a built-in IMAP address from a provider name unless an explicit address is supplied.
func ResolveIMAPAddress(provider string, address string) (string, error) {
	return internalimap.ResolveIMAPAddress(provider, address)
}

// NewClient constructs a single-account delete client.
func NewClient(config Config) (*Client, error) {
	imapClient, err := internalimap.NewClient(config)
	if err != nil {
		return nil, err
	}

	return &Client{imap: imapClient}, nil
}

// Delete deletes messages that match the supplied criteria for this client account.
func (c *Client) Delete(ctx context.Context, criteria DeleteCriteria) (DeleteResult, error) {
	if c == nil || c.imap == nil {
		return DeleteResult{}, ErrClientRequired
	}

	return c.imap.Delete(ctx, internalimap.DeleteCriteria{
		ReceivedBefore: criteria.ReceivedBefore,
		FromAccounts:   criteria.FromAccounts,
	})
}
