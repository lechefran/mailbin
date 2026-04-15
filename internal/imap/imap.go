package imap

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	gmailTrashMailbox = "[Gmail]/Trash"

	imapAddressAOL       = "imap.aol.com:993"
	imapAddressAOLExport = "export.imap.aol.com:993"
	imapAddressGmail     = "imap.gmail.com:993"
	imapAddressICloud    = "imap.mail.me.com:993"
	imapAddressOutlook   = "outlook.office365.com:993"
	imapAddressYahoo     = "imap.mail.yahoo.com:993"
	imapAddressZoho      = "imap.zoho.com:993"

	DefaultDeletePasses          = 3
	DefaultDeleteMailboxAttempts = 5
	DefaultDeleteCommandRetries  = 5
	DefaultDeleteStoreRetries    = 5
	DefaultDeleteExpungeRetries  = 5
	DefaultMailboxCommandRetries = 3
	DefaultFetchRetryAttempts    = 3
	DefaultBatchSize             = 20
	deleteRetryBackoff           = time.Second
)

const (
	AOL        = imapAddressAOL
	AOL_EXPORT = imapAddressAOLExport
	GMAIL      = imapAddressGmail
	ICLOUD     = imapAddressICloud
	OUTLOOK    = imapAddressOutlook
	YAHOO      = imapAddressYahoo
	ZOHO       = imapAddressZoho
)

var providerIMAPAddresses = map[string]string{
	"aol":          imapAddressAOL,
	"aol-export":   imapAddressAOLExport,
	"aolexport":    imapAddressAOLExport,
	"gmail":        imapAddressGmail,
	"googlemail":   imapAddressGmail,
	"hotmail":      imapAddressOutlook,
	"icloud":       imapAddressICloud,
	"live":         imapAddressOutlook,
	"microsoft365": imapAddressOutlook,
	"office365":    imapAddressOutlook,
	"outlook":      imapAddressOutlook,
	"yahoo":        imapAddressYahoo,
	"zoho":         imapAddressZoho,
}

var (
	ErrIMAPAddressOrProviderRequired = errors.New("imap address or provider is required")
	ErrUnsupportedProvider           = errors.New("unsupported provider")
	ErrClientRequired                = errors.New("client is required")
	ErrIMAPAddressRequired           = errors.New("imap address is required")
	ErrEmailRequired                 = errors.New("email is required")
	ErrPasswordRequired              = errors.New("password is required")
	ErrReceivedBeforeRequired        = errors.New("received before is required")
	ErrLoginFailed                   = errors.New("login failed")
	ErrDeleteIncomplete              = errors.New("delete incomplete")
	ErrNegativeConfigValue           = errors.New("configuration value cannot be negative")
)

type Config struct {
	Provider       string
	Address        string
	Email          string
	Password       string
	Logf           func(format string, args ...any)
	TLSConfig      *tls.Config
	DialTLSContext func(context.Context, string, *tls.Config) (net.Conn, error)
	LookupIPAddrs  func(context.Context, string) ([]net.IPAddr, error)

	// DeletePasses controls full delete passes across all mailboxes. Defaults to DefaultDeletePasses.
	DeletePasses int
	// DeleteMailboxAttempts controls attempts per mailbox delete flow. Defaults to DefaultDeleteMailboxAttempts.
	DeleteMailboxAttempts int
	// DeleteCommandRetries controls retries for mailbox-select/search commands. Defaults to DefaultDeleteCommandRetries.
	DeleteCommandRetries int
	// DeleteStoreRetries controls retries for UID STORE/UID MOVE commands. Defaults to DefaultDeleteStoreRetries.
	DeleteStoreRetries int
	// DeleteExpungeRetries controls retries for EXPUNGE commands. Defaults to DefaultDeleteExpungeRetries.
	DeleteExpungeRetries int
	// MailboxCommandRetries controls retries for LIST/SELECT mailbox commands. Defaults to DefaultMailboxCommandRetries.
	MailboxCommandRetries int
	// FetchRetryAttempts controls retries for FETCH summary batches. Defaults to DefaultFetchRetryAttempts.
	FetchRetryAttempts int
	// BatchSize controls FETCH and delete processing batch size. Defaults to DefaultBatchSize.
	BatchSize int
}

type Client struct {
	config Config
	retry  retryConfig
}

type retryConfig struct {
	deletePasses          int
	deleteMailboxAttempts int
	deleteCommandRetries  int
	deleteStoreRetries    int
	deleteExpungeRetries  int
	mailboxCommandRetries int
	fetchRetryAttempts    int
	batchSize             int
}

type session struct {
	conn           net.Conn
	reader         *bufio.Reader
	writer         *bufio.Writer
	nextTag        int
	commandTimeout time.Duration
	timedOut       bool
	client         *Client
}

type MessageSummary struct {
	Mailbox        string
	MessageID      string
	SequenceNumber int
	UID            uint32
	Flagged        bool
	ReceivedAt     time.Time
	Subject        string
	From           string
	To             string
}

type DeleteResult struct {
	Deleted                     []MessageSummary
	Incomplete                  bool
	RemainingMatchingCount      int
	RemainingMatchingCountKnown bool
	SkippedMailboxCount         int
}

type DeleteIncompleteError struct {
	RemainingMatchingCount      int
	RemainingMatchingCountKnown bool
	SkippedMailboxCount         int
	Cause                       error
}

func (e *DeleteIncompleteError) Error() string {
	if e == nil {
		return ErrDeleteIncomplete.Error()
	}

	switch {
	case e.RemainingMatchingCountKnown:
		return fmt.Sprintf("%s: %d emails still match delete criteria", ErrDeleteIncomplete, e.RemainingMatchingCount)
	case e.SkippedMailboxCount > 0:
		return fmt.Sprintf("%s: unable to verify %d mailbox(es)", ErrDeleteIncomplete, e.SkippedMailboxCount)
	case e.Cause != nil:
		return fmt.Sprintf("%s: %v", ErrDeleteIncomplete, e.Cause)
	default:
		return ErrDeleteIncomplete.Error()
	}
}

func (e *DeleteIncompleteError) Unwrap() []error {
	if e == nil {
		return nil
	}

	errs := []error{ErrDeleteIncomplete}
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}

	return errs
}

type imapResponseLine struct {
	line    string
	literal []byte
}

func NewClient(config Config) (*Client, error) {
	config.Provider = strings.TrimSpace(config.Provider)
	config.Email = strings.TrimSpace(config.Email)

	address, err := ResolveIMAPAddress(config.Provider, config.Address)
	if err != nil {
		return nil, err
	}
	config.Address = address
	if err := validateNonNegativeTuningConfig(config); err != nil {
		return nil, err
	}

	client := &Client{
		config: config,
		retry:  effectiveRetryConfig(config),
	}
	if err := client.validate(); err != nil {
		return nil, err
	}

	return client, nil
}

func ResolveIMAPAddress(provider string, address string) (string, error) {
	address = strings.TrimSpace(address)
	if address != "" {
		return address, nil
	}

	normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
	if normalizedProvider == "" {
		return "", ErrIMAPAddressOrProviderRequired
	}

	resolvedAddress, ok := providerIMAPAddresses[normalizedProvider]
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnsupportedProvider, provider)
	}

	return resolvedAddress, nil
}

func effectiveRetryConfig(config Config) retryConfig {
	return retryConfig{
		deletePasses:          withPositiveDefault(config.DeletePasses, DefaultDeletePasses),
		deleteMailboxAttempts: withPositiveDefault(config.DeleteMailboxAttempts, DefaultDeleteMailboxAttempts),
		deleteCommandRetries:  withPositiveDefault(config.DeleteCommandRetries, DefaultDeleteCommandRetries),
		deleteStoreRetries:    withPositiveDefault(config.DeleteStoreRetries, DefaultDeleteStoreRetries),
		deleteExpungeRetries:  withPositiveDefault(config.DeleteExpungeRetries, DefaultDeleteExpungeRetries),
		mailboxCommandRetries: withPositiveDefault(config.MailboxCommandRetries, DefaultMailboxCommandRetries),
		fetchRetryAttempts:    withPositiveDefault(config.FetchRetryAttempts, DefaultFetchRetryAttempts),
		batchSize:             withPositiveDefault(config.BatchSize, DefaultBatchSize),
	}
}

func withPositiveDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}

	return fallback
}

func validateNonNegativeTuningConfig(config Config) error {
	validatedValues := []struct {
		name  string
		value int
	}{
		{name: "DeletePasses", value: config.DeletePasses},
		{name: "DeleteMailboxAttempts", value: config.DeleteMailboxAttempts},
		{name: "DeleteCommandRetries", value: config.DeleteCommandRetries},
		{name: "DeleteStoreRetries", value: config.DeleteStoreRetries},
		{name: "DeleteExpungeRetries", value: config.DeleteExpungeRetries},
		{name: "MailboxCommandRetries", value: config.MailboxCommandRetries},
		{name: "FetchRetryAttempts", value: config.FetchRetryAttempts},
		{name: "BatchSize", value: config.BatchSize},
	}

	for _, validatedValue := range validatedValues {
		if validatedValue.value < 0 {
			return fmt.Errorf("%w: %s", ErrNegativeConfigValue, validatedValue.name)
		}
	}

	return nil
}

func (c *Client) login(ctx context.Context) (*session, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}

	email, err := quoteIMAPString(c.config.Email)
	if err != nil {
		return nil, fmt.Errorf("invalid email: %w", err)
	}

	password, err := quoteIMAPString(c.config.Password)
	if err != nil {
		return nil, fmt.Errorf("invalid password: %w", err)
	}

	host, port, err := net.SplitHostPort(c.config.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid IMAP address %q: %w", c.config.Address, err)
	}

	tlsConfig := c.cloneTLSConfig(host)
	conn, err := c.connect(ctx, host, port, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("connect to IMAP server: %w", err)
	}

	session := &session{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		writer:  bufio.NewWriter(conn),
		nextTag: 1,
		client:  c,
	}

	if deadline, ok := ctx.Deadline(); ok {
		session.commandTimeout = time.Until(deadline)
	}

	if err := session.expectGreeting(); err != nil {
		conn.Close()
		return nil, err
	}

	if _, _, err := session.runCommand("LOGIN %s %s", email, password); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrLoginFailed, err)
	}

	return session, nil
}

func (c *Client) validate() error {
	switch {
	case c == nil:
		return ErrClientRequired
	case c.config.Address == "":
		return ErrIMAPAddressRequired
	case c.config.Email == "":
		return ErrEmailRequired
	case c.config.Password == "":
		return ErrPasswordRequired
	default:
		return nil
	}
}

func (c *Client) logf(format string, args ...any) {
	if c == nil || c.config.Logf == nil {
		return
	}

	c.config.Logf(format, args...)
}

func (s *session) logf(format string, args ...any) {
	if s == nil || s.client == nil {
		return
	}

	s.client.logf(format, args...)
}

func (s *session) retryConfig() retryConfig {
	if s == nil || s.client == nil {
		return effectiveRetryConfig(Config{})
	}

	return s.client.retryConfig()
}

func (c *Client) retryConfig() retryConfig {
	if c == nil {
		return effectiveRetryConfig(Config{})
	}
	if c.retry == (retryConfig{}) {
		return effectiveRetryConfig(c.config)
	}

	return c.retry
}

func (c *Client) cloneTLSConfig(serverName string) *tls.Config {
	if c.config.TLSConfig == nil {
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
		}
	}

	config := c.config.TLSConfig.Clone()
	if config.MinVersion == 0 {
		config.MinVersion = tls.VersionTLS12
	}
	if config.ServerName == "" {
		config.ServerName = serverName
	}

	return config
}

func (c *Client) connect(ctx context.Context, host, port string, tlsConfig *tls.Config) (net.Conn, error) {
	address := net.JoinHostPort(host, port)

	conn, err := c.dialTLS(ctx, address, tlsConfig)
	if err == nil {
		return conn, nil
	}
	if !isDNSLookupError(err) {
		return nil, err
	}

	conn, fallbackErr := c.resolveAndDialTLS(ctx, host, port, tlsConfig)
	if fallbackErr == nil {
		return conn, nil
	}

	return nil, fmt.Errorf("%w; lookup fallback failed: %v", err, fallbackErr)
}

func (c *Client) dialTLS(ctx context.Context, address string, tlsConfig *tls.Config) (net.Conn, error) {
	if c != nil && c.config.DialTLSContext != nil {
		return c.config.DialTLSContext(ctx, address, tlsConfig)
	}

	dialer := &tls.Dialer{Config: tlsConfig}
	return dialer.DialContext(ctx, "tcp", address)
}

func (c *Client) resolveAndDialTLS(ctx context.Context, host, port string, tlsConfig *tls.Config) (net.Conn, error) {
	ipAddrs, err := c.lookupIPAddrs(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve host %s: %w", host, err)
	}
	if len(ipAddrs) == 0 {
		return nil, fmt.Errorf("resolve host %s: no IP addresses returned", host)
	}

	var lastErr error
	for _, ipAddr := range ipAddrs {
		conn, err := c.dialTLSByIP(ctx, ipAddr.IP.String(), port, host, tlsConfig)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return nil, lastErr
	}

	return nil, fmt.Errorf("resolve host %s: no IP addresses returned", host)
}

func (c *Client) lookupIPAddrs(ctx context.Context, host string) ([]net.IPAddr, error) {
	if c != nil && c.config.LookupIPAddrs != nil {
		return c.config.LookupIPAddrs(ctx, host)
	}

	resolver := &net.Resolver{PreferGo: true}
	return resolver.LookupIPAddr(ctx, host)
}

func (c *Client) dialTLSByIP(ctx context.Context, ip, port, serverName string, tlsConfig *tls.Config) (net.Conn, error) {
	dialer := &net.Dialer{}
	rawConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
	if err != nil {
		return nil, err
	}

	config := tlsConfig.Clone()
	if config.ServerName == "" {
		config.ServerName = serverName
	}

	tlsConn := tls.Client(rawConn, config)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, err
	}

	return tlsConn, nil
}

func (s *session) Logout() error {
	if s == nil || s.conn == nil {
		return nil
	}

	_, _, commandErr := s.runCommand("LOGOUT")
	closeErr := s.conn.Close()
	s.conn = nil

	if commandErr != nil {
		return commandErr
	}

	return closeErr
}

func (s *session) deleteBefore(before time.Time) ([]MessageSummary, error) {
	result, err := s.deleteBeforeResult(before)
	return result.Deleted, err
}

func (s *session) deleteBeforeResult(before time.Time) (DeleteResult, error) {
	if before.IsZero() {
		return DeleteResult{}, ErrReceivedBeforeRequired
	}

	return s.deleteMatchingMessages("BEFORE %s UNFLAGGED", formatIMAPDate(before))
}

func (c *Client) DeleteBefore(ctx context.Context, before time.Time) (DeleteResult, error) {
	if c == nil {
		return DeleteResult{}, ErrClientRequired
	}
	if before.IsZero() {
		return DeleteResult{}, ErrReceivedBeforeRequired
	}

	session, err := c.login(ctx)
	if err != nil {
		return DeleteResult{}, err
	}

	result, deleteErr := session.deleteBeforeResult(before)
	logoutErr := session.Logout()

	if deleteErr != nil {
		return result, deleteErr
	}
	if logoutErr != nil && !isTimeoutError(logoutErr) {
		return result, logoutErr
	}

	return result, nil
}

func (s *session) deleteMatchingMessages(format string, args ...any) (DeleteResult, error) {
	retry := s.retryConfig()
	var deletedSummaries []MessageSummary
	var firstErr error

deletePasses:
	for pass := 0; pass < retry.deletePasses; pass++ {
		mailboxes, err := s.listMailboxes()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		mailboxes = prioritizeDeleteMailboxes(mailboxes, s.isGmailAccount())
		s.logf("delete pass %d/%d: scanning %d mailboxes", pass+1, retry.deletePasses, len(mailboxes))

		passDeletedCount := 0
		passMovedToTrashCount := 0
		skippedMailboxCount := 0
		successfulMailboxScans := 0
		for _, mailbox := range mailboxes {
			s.logf("delete pass %d: scanning mailbox %s", pass+1, mailbox)
			mailboxSummaries, movedToTrashCount, hadSearchResults, err := s.deleteMailboxWithRetry(mailbox, format, args...)
			if err != nil {
				if isRetryableConnectionError(err) {
					s.logf("skipping mailbox %s after retryable connection error during delete: %v", mailbox, err)
					skippedMailboxCount++
					if reconnectErr := s.reconnect(); reconnectErr != nil {
						if firstErr == nil && len(deletedSummaries) == 0 {
							firstErr = reconnectErr
						}
						break deletePasses
					}
					continue
				}
				if firstErr == nil {
					firstErr = err
				}
				skippedMailboxCount++
				continue
			}
			successfulMailboxScans++
			if !hadSearchResults {
				s.logf("delete pass %d: mailbox %s had no matching emails", pass+1, mailbox)
				continue
			}

			passDeletedCount += len(mailboxSummaries)
			passMovedToTrashCount += movedToTrashCount
			s.logf(
				"delete pass %d: mailbox %s deleted %d emails and moved %d to trash",
				pass+1,
				mailbox,
				len(mailboxSummaries),
				movedToTrashCount,
			)
			deletedSummaries = append(deletedSummaries, mailboxSummaries...)
		}

		if successfulMailboxScans == 0 && firstErr != nil && len(deletedSummaries) == 0 {
			if isRetryableConnectionError(firstErr) {
				s.logf("delete pass %d: only retryable connection errors encountered; returning incomplete result: %v", pass+1, firstErr)
				return DeleteResult{Incomplete: true}, &DeleteIncompleteError{Cause: firstErr}
			}
			return DeleteResult{}, firstErr
		}

		if passDeletedCount == 0 && passMovedToTrashCount == 0 {
			s.logf(
				"delete pass %d: no delete or move progress found; stopping (skipped_mailboxes=%d)",
				pass+1,
				skippedMailboxCount,
			)
			break
		}
		s.logf(
			"delete pass %d: deleted %d emails, moved %d emails to trash, skipped %d mailboxes",
			pass+1,
			passDeletedCount,
			passMovedToTrashCount,
			skippedMailboxCount,
		)
	}

	if len(deletedSummaries) == 0 && firstErr != nil {
		if isRetryableConnectionError(firstErr) {
			s.logf("delete completed with only retryable connection errors; returning incomplete result: %v", firstErr)
			return DeleteResult{Incomplete: true}, &DeleteIncompleteError{Cause: firstErr}
		}
		return DeleteResult{}, firstErr
	}

	deletedSummaries = dedupeMessageSummaries(deletedSummaries)
	result := DeleteResult{Deleted: deletedSummaries}
	remainingCount, skippedMailboxCount, err := s.checkDeleteCompletion(format, args...)
	if err != nil {
		s.logf("delete incomplete: unable to verify all mailboxes: %v", err)
		result.Incomplete = true
		result.SkippedMailboxCount = skippedMailboxCount
		return result, &DeleteIncompleteError{
			SkippedMailboxCount: skippedMailboxCount,
			Cause:               err,
		}
	}
	if remainingCount > 0 || skippedMailboxCount > 0 {
		result.Incomplete = true
		result.RemainingMatchingCount = remainingCount
		result.RemainingMatchingCountKnown = remainingCount > 0
		result.SkippedMailboxCount = skippedMailboxCount
		return result, &DeleteIncompleteError{
			RemainingMatchingCount:      remainingCount,
			RemainingMatchingCountKnown: remainingCount > 0,
			SkippedMailboxCount:         skippedMailboxCount,
		}
	}

	return result, nil
}

func (s *session) checkDeleteCompletion(format string, args ...any) (int, int, error) {
	mailboxes, err := s.listMailboxes()
	if err != nil {
		return 0, 0, fmt.Errorf("list mailboxes for delete verification: %w", err)
	}

	mailboxes = prioritizeDeleteMailboxes(mailboxes, s.isGmailAccount())
	remainingCount := 0
	skippedMailboxCount := 0
	for _, mailbox := range mailboxes {
		if err := s.deleteSelectMailboxWithRetry(mailbox); err != nil {
			if isRetryableConnectionError(err) {
				return remainingCount, skippedMailboxCount, fmt.Errorf("select mailbox %s for delete verification: %w", mailbox, err)
			}
			skippedMailboxCount++
			s.logf("delete verification: skipping mailbox %s after select error: %v", mailbox, err)
			continue
		}

		uids, err := s.deleteSearchUIDsWithRetry(mailbox, format, args...)
		if err != nil {
			if isRetryableConnectionError(err) {
				return remainingCount, skippedMailboxCount, fmt.Errorf("search mailbox %s for delete verification: %w", mailbox, err)
			}
			skippedMailboxCount++
			s.logf("delete verification: skipping mailbox %s after search error: %v", mailbox, err)
			continue
		}
		if len(uids) == 0 {
			continue
		}

		summaries, err := s.fetchMessageSummariesByUID(mailbox, uids)
		if err != nil {
			if isRetryableConnectionError(err) {
				return remainingCount, skippedMailboxCount, fmt.Errorf("fetch mailbox %s for delete verification: %w", mailbox, err)
			}
			skippedMailboxCount++
			s.logf("delete verification: skipping mailbox %s after fetch error: %v", mailbox, err)
			continue
		}

		mailboxRemainingCount := 0
		for _, summary := range summaries {
			if summary.Flagged {
				continue
			}
			mailboxRemainingCount++
		}
		if mailboxRemainingCount == 0 {
			continue
		}

		remainingCount += mailboxRemainingCount
		s.logf(
			"delete incomplete: mailbox %s still has %d emails matching delete criteria",
			mailbox,
			mailboxRemainingCount,
		)
	}

	if remainingCount > 0 {
		s.logf(
			"delete incomplete: %d emails still match delete criteria across account (skipped_mailboxes=%d)",
			remainingCount,
			skippedMailboxCount,
		)
		return remainingCount, skippedMailboxCount, nil
	}

	s.logf("delete complete: no emails matching delete criteria remain across account (skipped_mailboxes=%d)", skippedMailboxCount)
	return 0, skippedMailboxCount, nil
}

func (s *session) deleteMailboxWithRetry(mailbox string, format string, args ...any) ([]MessageSummary, int, bool, error) {
	retry := s.retryConfig()
	var lastTimeoutErr error
	deletedSummaries := make([]MessageSummary, 0)
	movedToTrashCount := 0
	hadSearchResults := false

attemptLoop:
	for attempt := 0; attempt < retry.deleteMailboxAttempts; attempt++ {
		s.logf("delete mailbox %s: attempt %d", mailbox, attempt+1)
		if err := s.deleteSelectMailboxWithRetry(mailbox); err != nil {
			return deletedSummaries, movedToTrashCount, false, err
		}

		uids, err := s.deleteSearchUIDsWithRetry(mailbox, format, args...)
		if err != nil {
			return deletedSummaries, movedToTrashCount, false, fmt.Errorf("search mailbox %s: %w", mailbox, err)
		}
		if len(uids) == 0 {
			if len(deletedSummaries) > 0 {
				return deletedSummaries, movedToTrashCount, true, nil
			}
			return nil, movedToTrashCount, false, nil
		}
		hadSearchResults = true
		s.logf("delete mailbox %s: matched %d emails", mailbox, len(uids))

		batchSize := retry.batchSize

		attemptDeletedSummaries := make([]MessageSummary, 0, len(uids))
		attemptMovedToTrashCount := 0
		totalBatches := (len(uids) + batchSize - 1) / batchSize
		for start := 0; start < len(uids); start += batchSize {
			end := start + batchSize
			if end > len(uids) {
				end = len(uids)
			}

			s.logf(
				"mailbox %s: streaming delete batch %d/%d (%d uids)",
				mailbox,
				(start/batchSize)+1,
				totalBatches,
				end-start,
			)

			summaries, err := s.fetchMessageSummariesByUID(mailbox, uids[start:end])
			if err != nil {
				if !isRetryableConnectionError(err) {
					return deletedSummaries, movedToTrashCount, true, err
				}

				lastTimeoutErr = err
				s.logf("retrying mailbox %s delete after timeout: %v", mailbox, err)
				if attempt == retry.deleteMailboxAttempts-1 {
					continue attemptLoop
				}
				sleepRetryBackoff(deleteRetryBackoff)
				if reconnectErr := s.reconnectAndSelectMailbox(mailbox); reconnectErr != nil {
					return deletedSummaries, movedToTrashCount, true, reconnectErr
				}
				continue attemptLoop
			}

			deletedBatch, movedToTrashCount := s.deleteSelectedMailboxMessages(summaries)
			attemptDeletedSummaries = append(attemptDeletedSummaries, deletedBatch...)
			attemptMovedToTrashCount += movedToTrashCount
		}

		if attemptMovedToTrashCount > 0 {
			s.logf(
				"delete mailbox %s: moved %d emails to trash (not counted as deleted)",
				mailbox,
				attemptMovedToTrashCount,
			)
		}

		deletedSummaries = append(deletedSummaries, attemptDeletedSummaries...)
		movedToTrashCount += attemptMovedToTrashCount
		return deletedSummaries, movedToTrashCount, true, nil
	}

	if len(deletedSummaries) > 0 || movedToTrashCount > 0 {
		s.logf(
			"mailbox %s: timed out during delete but retained partial progress deleted=%d moved_to_trash=%d",
			mailbox,
			len(deletedSummaries),
			movedToTrashCount,
		)
		return deletedSummaries, movedToTrashCount, hadSearchResults, nil
	}

	if lastTimeoutErr != nil {
		return nil, 0, hadSearchResults, lastTimeoutErr
	}

	return nil, 0, hadSearchResults, nil
}

func (s *session) deleteSelectedMailboxMessages(summaries []MessageSummary) ([]MessageSummary, int) {
	if len(summaries) == 0 {
		return nil, 0
	}

	orderedSummaries := append([]MessageSummary(nil), summaries...)

	mailbox := orderedSummaries[0].Mailbox
	moveToTrashFirst := s.shouldMoveAllMailToTrash(mailbox)
	batchSize := s.retryConfig().batchSize

	deletedSummaries := make([]MessageSummary, 0, len(orderedSummaries))
	pendingExpunge := make([]MessageSummary, 0, batchSize)
	movedToTrashCount := 0
	totalBatches := (len(orderedSummaries) + batchSize - 1) / batchSize

	for batchStart := 0; batchStart < len(orderedSummaries); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(orderedSummaries) {
			batchEnd = len(orderedSummaries)
		}
		batch := orderedSummaries[batchStart:batchEnd]
		s.logf(
			"mailbox %s: processing delete batch %d/%d (%d emails)",
			mailbox,
			(batchStart/batchSize)+1,
			totalBatches,
			len(batch),
		)

		for _, summary := range batch {
			subject := summary.Subject
			if subject == "" {
				subject = "-"
			}
			receivedAt := "unknown-time"
			if !summary.ReceivedAt.IsZero() {
				receivedAt = summary.ReceivedAt.Format(time.RFC3339)
			}
			s.logf(
				"deleting email mailbox=%s seq=%d uid=%d received_at=%s message_id=%q subject=%q",
				summary.Mailbox,
				summary.SequenceNumber,
				summary.UID,
				receivedAt,
				summary.MessageID,
				subject,
			)

			if summary.Flagged {
				s.logf(
					"skipping email mailbox=%s seq=%d uid=%d received_at=%s message_id=%q subject=%q: flagged/starred",
					summary.Mailbox,
					summary.SequenceNumber,
					summary.UID,
					receivedAt,
					summary.MessageID,
					subject,
				)
				continue
			}

			if moveToTrashFirst {
				moved, err := s.moveMessageToTrashByUIDWithRetry(summary, gmailTrashMailbox)
				if err != nil {
					s.logf(
						"skipping email mailbox=%s seq=%d uid=%d received_at=%s message_id=%q subject=%q: move to trash failed: %v",
						summary.Mailbox,
						summary.SequenceNumber,
						summary.UID,
						receivedAt,
						summary.MessageID,
						subject,
						err,
					)
					continue
				}
				if !moved {
					s.logf(
						"skipping email mailbox=%s seq=%d uid=%d received_at=%s message_id=%q subject=%q: unable to move to trash",
						summary.Mailbox,
						summary.SequenceNumber,
						summary.UID,
						receivedAt,
						summary.MessageID,
						subject,
					)
					continue
				}

				s.logf(
					"moved email to trash mailbox=%s uid=%d received_at=%s message_id=%q subject=%q",
					summary.Mailbox,
					summary.UID,
					receivedAt,
					summary.MessageID,
					subject,
				)
				movedToTrashCount++
				continue
			}

			if err := s.storeDeletedFlagByUIDWithRetry(summary); err != nil {
				s.logf(
					"skipping email mailbox=%s seq=%d uid=%d received_at=%s message_id=%q subject=%q: store deleted flag failed: %v",
					summary.Mailbox,
					summary.SequenceNumber,
					summary.UID,
					receivedAt,
					summary.MessageID,
					subject,
					err,
				)
				continue
			}
			pendingExpunge = append(pendingExpunge, summary)
		}

		if len(pendingExpunge) == 0 {
			continue
		}

		if err := s.expungeMailboxWithRetry(mailbox); err != nil {
			s.logf(
				"mailbox %s: expunge failed for %d pending deletes: %v",
				mailbox,
				len(pendingExpunge),
				err,
			)
			continue
		}
		deletedSummaries = append(deletedSummaries, pendingExpunge...)
		pendingExpunge = pendingExpunge[:0]
	}

	if len(pendingExpunge) > 0 {
		if err := s.expungeMailboxWithRetry(mailbox); err != nil {
			s.logf(
				"skipping %d pending deletes in mailbox=%s after final expunge failure: %v",
				len(pendingExpunge),
				mailbox,
				err,
			)
		} else {
			deletedSummaries = append(deletedSummaries, pendingExpunge...)
		}
	}

	return deletedSummaries, movedToTrashCount
}

func (s *session) storeDeletedFlagByUIDWithRetry(summary MessageSummary) error {
	retry := s.retryConfig()
	var lastErr error
	for attempt := 0; attempt < retry.deleteStoreRetries; attempt++ {
		err := s.storeDeletedFlagByUID(summary.UID)
		if err == nil {
			return nil
		}
		if !isRetryableConnectionError(err) {
			return err
		}

		lastErr = err
		s.logf(
			"retrying UID STORE for mailbox %s uid=%d after timeout (attempt %d/%d): %v",
			summary.Mailbox,
			summary.UID,
			attempt+1,
			retry.deleteStoreRetries,
			err,
		)
		if attempt == retry.deleteStoreRetries-1 {
			break
		}
		sleepRetryBackoff(deleteRetryBackoff)
		if err := s.reconnectAndSelectMailbox(summary.Mailbox); err != nil {
			return err
		}
	}

	if lastErr != nil {
		return lastErr
	}

	return fmt.Errorf("uid store failed")
}

func (s *session) storeDeletedFlagByUID(uid uint32) error {
	if uid == 0 {
		return fmt.Errorf("uid is required")
	}

	_, _, err := s.runCommand(`UID STORE %d +FLAGS.SILENT (\Deleted)`, uid)
	return err
}

func (s *session) moveMessageToTrashByUID(uid uint32, trashMailbox string) error {
	if uid == 0 {
		return fmt.Errorf("uid is required")
	}

	quotedTrashMailbox, err := quoteIMAPString(trashMailbox)
	if err != nil {
		return err
	}

	_, _, err = s.runCommand(`UID MOVE %d %s`, uid, quotedTrashMailbox)
	return err
}

func (s *session) moveMessageToTrashByUIDWithRetry(summary MessageSummary, trashMailbox string) (bool, error) {
	retry := s.retryConfig()
	var lastErr error
	for attempt := 0; attempt < retry.deleteStoreRetries; attempt++ {
		err := s.moveMessageToTrashByUID(summary.UID, trashMailbox)
		if err == nil {
			return true, nil
		}
		if isUnsupportedMoveError(err) {
			s.logf(
				"UID MOVE unsupported for mailbox=%s uid=%d; skipping all-mail delete for this message",
				summary.Mailbox,
				summary.UID,
			)
			return false, nil
		}
		if !isRetryableConnectionError(err) {
			return false, err
		}

		lastErr = err
		s.logf(
			"retrying UID MOVE for mailbox %s uid=%d after timeout (attempt %d/%d): %v",
			summary.Mailbox,
			summary.UID,
			attempt+1,
			retry.deleteStoreRetries,
			err,
		)
		if attempt == retry.deleteStoreRetries-1 {
			break
		}
		sleepRetryBackoff(deleteRetryBackoff)
		if err := s.reconnectAndSelectMailbox(summary.Mailbox); err != nil {
			return false, err
		}
	}

	if lastErr != nil {
		return false, lastErr
	}

	return false, fmt.Errorf("uid move failed")
}

func (s *session) reconnectAndSelectMailbox(mailbox string) error {
	if err := s.reconnect(); err != nil {
		return err
	}
	if err := s.selectMailbox(mailbox); err != nil {
		return err
	}

	return nil
}

func (s *session) expungeMailboxWithRetry(mailbox string) error {
	retry := s.retryConfig()
	var lastErr error
	for attempt := 0; attempt < retry.deleteExpungeRetries; attempt++ {
		_, _, err := s.runCommand("EXPUNGE")
		if err == nil {
			return nil
		}
		if !isRetryableConnectionError(err) {
			return err
		}

		lastErr = err
		s.logf(
			"retrying EXPUNGE for mailbox %s after timeout (attempt %d/%d): %v",
			mailbox,
			attempt+1,
			retry.deleteExpungeRetries,
			err,
		)
		if attempt == retry.deleteExpungeRetries-1 {
			break
		}
		sleepRetryBackoff(deleteRetryBackoff)
		if err := s.reconnectAndSelectMailbox(mailbox); err != nil {
			return err
		}
	}

	if lastErr != nil {
		return lastErr
	}

	return fmt.Errorf("expunge failed")
}

func (s *session) listMailboxes() ([]string, error) {
	retry := s.retryConfig()
	var lines []imapResponseLine
	var err error
	for attempt := 0; attempt < retry.mailboxCommandRetries; attempt++ {
		lines, _, err = s.runCommand(`LIST "" "*"`)
		if err == nil {
			break
		}
		if !isRetryableConnectionError(err) {
			return nil, fmt.Errorf("list mailboxes: %w", err)
		}

		s.logf(
			"retrying LIST after retryable connection error (attempt %d/%d): %v",
			attempt+1,
			retry.mailboxCommandRetries,
			err,
		)
		if attempt == retry.mailboxCommandRetries-1 {
			return nil, fmt.Errorf("list mailboxes: %w", err)
		}
		if reconnectErr := s.reconnect(); reconnectErr != nil {
			return nil, fmt.Errorf("list mailboxes reconnect: %w", reconnectErr)
		}
	}

	var mailboxes []string
	for _, responseLine := range lines {
		if !strings.HasPrefix(responseLine.line, "* LIST ") {
			continue
		}

		mailbox, selectable, err := parseListMailbox(responseLine.line)
		if err != nil {
			return nil, err
		}
		if !selectable {
			continue
		}

		mailboxes = append(mailboxes, mailbox)
	}

	if len(mailboxes) == 0 {
		return nil, fmt.Errorf("no selectable mailboxes found")
	}

	sort.SliceStable(mailboxes, func(i, j int) bool {
		leftPriority := mailboxPriority(mailboxes[i])
		rightPriority := mailboxPriority(mailboxes[j])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}

		return strings.ToLower(mailboxes[i]) < strings.ToLower(mailboxes[j])
	})

	return mailboxes, nil
}

func (s *session) selectMailbox(mailbox string) error {
	quotedMailbox, err := quoteIMAPString(mailbox)
	if err != nil {
		return err
	}

	_, _, err = s.runCommand("SELECT %s", quotedMailbox)
	if err != nil {
		return fmt.Errorf("select mailbox %s: %w", mailbox, err)
	}

	return nil
}

func (s *session) selectMailboxWithRetry(mailbox string) error {
	return s.selectMailboxWithRetryConfig(mailbox, s.retryConfig().mailboxCommandRetries, 0)
}

func (s *session) deleteSelectMailboxWithRetry(mailbox string) error {
	return s.selectMailboxWithRetryConfig(mailbox, s.retryConfig().deleteCommandRetries, deleteRetryBackoff)
}

func (s *session) selectMailboxWithRetryConfig(mailbox string, maxAttempts int, backoff time.Duration) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := s.selectMailbox(mailbox); err == nil {
			return nil
		} else if !isRetryableConnectionError(err) {
			return err
		} else {
			lastErr = err
		}

		s.logf(
			"retrying SELECT for mailbox %s after retryable connection error (attempt %d/%d): %v",
			mailbox,
			attempt+1,
			maxAttempts,
			lastErr,
		)
		if attempt == maxAttempts-1 {
			break
		}
		sleepRetryBackoff(backoff)
		if err := s.reconnect(); err != nil {
			return err
		}
	}

	if lastErr != nil {
		return lastErr
	}

	return fmt.Errorf("select mailbox %s failed", mailbox)
}

func (s *session) searchUIDs(format string, args ...any) ([]uint32, error) {
	lines, _, err := s.runCommand("UID SEARCH "+format, args...)
	if err != nil {
		return nil, fmt.Errorf("search mailbox: %w", err)
	}

	uids, _ := parseSearchIDs(lines)
	if len(uids) == 0 {
		return nil, nil
	}

	return uids, nil
}

func parseSearchIDs(lines []imapResponseLine) ([]uint32, bool) {
	sawRecognized := false
	for _, responseLine := range lines {
		ids, lineRecognized := parseSearchIDsFromLine(responseLine.line)
		if !lineRecognized {
			continue
		}
		sawRecognized = true
		if len(ids) == 0 {
			continue
		}

		return ids, true
	}

	return nil, sawRecognized
}

func parseSearchIDsFromLine(line string) ([]uint32, bool) {
	trimmed := strings.TrimSpace(line)
	upper := strings.ToUpper(trimmed)
	switch {
	case strings.HasPrefix(upper, "* SEARCH"):
		fields := strings.Fields(trimmed)
		if len(fields) <= 2 {
			return nil, true
		}
		return parseIDTokens(fields[2:]), true
	case strings.HasPrefix(upper, "* ESEARCH"):
		return parseESearchIDs(trimmed), true
	default:
		return nil, false
	}
}

func parseESearchIDs(line string) []uint32 {
	upper := strings.ToUpper(line)
	allIndex := strings.Index(upper, " ALL ")
	if allIndex == -1 {
		return nil
	}

	afterAll := strings.TrimSpace(line[allIndex+len(" ALL "):])
	if afterAll == "" {
		return nil
	}

	return parseIDTokens(strings.Fields(afterAll))
}

func parseIDTokens(tokens []string) []uint32 {
	var ids []uint32
	for _, token := range tokens {
		expanded := expandIDToken(token)
		if len(expanded) == 0 {
			continue
		}
		ids = append(ids, expanded...)
	}

	return ids
}

func expandIDToken(token string) []uint32 {
	cleanToken := strings.Trim(token, "(),")
	if cleanToken == "" || strings.Contains(cleanToken, "*") {
		return nil
	}

	parts := strings.Split(cleanToken, ",")
	var ids []uint32
	for _, part := range parts {
		segment := strings.TrimSpace(part)
		if segment == "" {
			continue
		}

		if strings.Contains(segment, ":") {
			rangeIDs := expandIDRange(segment)
			if len(rangeIDs) == 0 {
				continue
			}
			ids = append(ids, rangeIDs...)
			continue
		}

		value, err := strconv.ParseUint(segment, 10, 32)
		if err != nil {
			continue
		}
		ids = append(ids, uint32(value))
	}

	return ids
}

func expandIDRange(segment string) []uint32 {
	rangeParts := strings.SplitN(segment, ":", 2)
	if len(rangeParts) != 2 {
		return nil
	}

	start, err := strconv.ParseUint(strings.TrimSpace(rangeParts[0]), 10, 32)
	if err != nil {
		return nil
	}
	end, err := strconv.ParseUint(strings.TrimSpace(rangeParts[1]), 10, 32)
	if err != nil {
		return nil
	}

	start32 := uint32(start)
	end32 := uint32(end)
	if start32 <= end32 {
		ids := make([]uint32, 0, int(end32-start32)+1)
		for value := start32; value <= end32; value++ {
			ids = append(ids, value)
		}
		return ids
	}

	ids := make([]uint32, 0, int(start32-end32)+1)
	for value := start32; value >= end32; value-- {
		ids = append(ids, value)
		if value == 0 {
			break
		}
	}
	return ids
}

func (s *session) deleteSearchUIDsWithRetry(mailbox string, format string, args ...any) ([]uint32, error) {
	return s.searchUIDsWithRetryConfig(mailbox, s.retryConfig().deleteCommandRetries, deleteRetryBackoff, format, args...)
}

func (s *session) searchUIDsWithRetryConfig(mailbox string, maxAttempts int, backoff time.Duration, format string, args ...any) ([]uint32, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		uids, err := s.searchUIDs(format, args...)
		if err == nil {
			return uids, nil
		}
		if !isRetryableConnectionError(err) {
			return nil, err
		}

		lastErr = err
		s.logf(
			"retrying UID SEARCH for mailbox %s after retryable connection error (attempt %d/%d): %v",
			mailbox,
			attempt+1,
			maxAttempts,
			err,
		)
		if attempt == maxAttempts-1 {
			break
		}
		sleepRetryBackoff(backoff)
		if reconnectErr := s.reconnectAndSelectMailbox(mailbox); reconnectErr != nil {
			return nil, reconnectErr
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}

	return nil, fmt.Errorf("uid search mailbox %s failed", mailbox)
}

func sleepRetryBackoff(delay time.Duration) {
	if delay <= 0 {
		return
	}

	time.Sleep(delay)
}

func (s *session) fetchMessageSummariesByUID(mailbox string, uids []uint32) ([]MessageSummary, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	return s.fetchMessageSummariesInBatches(
		mailbox,
		len(uids),
		func(start, end int) ([]MessageSummary, error) {
			return s.fetchMessageSummaryBatchByUID(mailbox, uids[start:end])
		},
	)
}

func (s *session) fetchMessageSummariesInBatches(
	mailbox string,
	total int,
	fetchBatch func(start, end int) ([]MessageSummary, error),
) ([]MessageSummary, error) {
	batchSize := s.retryConfig().batchSize

	summaries := make([]MessageSummary, 0, total)
	for start := 0; start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}

		batchSummaries, err := s.fetchBatchWithRetry(mailbox, start, end, fetchBatch)
		if err != nil {
			return nil, fmt.Errorf("fetch email summaries: %w", err)
		}
		summaries = append(summaries, batchSummaries...)
	}

	return summaries, nil
}

func (s *session) fetchBatchWithRetry(
	mailbox string,
	start int,
	end int,
	fetchBatch func(start, end int) ([]MessageSummary, error),
) ([]MessageSummary, error) {
	retry := s.retryConfig()
	var lastErr error
	for attempt := 0; attempt < retry.fetchRetryAttempts; attempt++ {
		batchSummaries, err := fetchBatch(start, end)
		if err == nil {
			return batchSummaries, nil
		}
		if !isRetryableConnectionError(err) || s.client == nil {
			return nil, err
		}

		lastErr = err
		s.logf(
			"retrying fetch for mailbox %s after timeout (attempt %d/%d): %v",
			mailbox,
			attempt+1,
			retry.fetchRetryAttempts,
			err,
		)
		if attempt == retry.fetchRetryAttempts-1 {
			break
		}
		if reconnectErr := s.reconnectAndSelectMailbox(mailbox); reconnectErr != nil {
			return nil, reconnectErr
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}

	return nil, fmt.Errorf("fetch batch failed")
}

func (s *session) fetchMessageSummaryBatchByUID(mailbox string, uids []uint32) ([]MessageSummary, error) {
	values := make([]string, 0, len(uids))
	for _, uid := range uids {
		values = append(values, strconv.FormatUint(uint64(uid), 10))
	}

	lines, _, err := s.runCommand(
		"UID FETCH %s (UID FLAGS INTERNALDATE BODY.PEEK[HEADER.FIELDS (MESSAGE-ID SUBJECT FROM TO)])",
		strings.Join(values, ","),
	)
	if err != nil {
		return nil, err
	}

	summaries := make([]MessageSummary, 0, len(uids))
	for _, responseLine := range lines {
		if !strings.HasPrefix(responseLine.line, "* ") || !strings.Contains(responseLine.line, " FETCH ") {
			continue
		}

		summary, err := parseFetchSummary(mailbox, responseLine)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}

	return summaries, nil
}

func (s *session) runCommand(format string, args ...any) ([]imapResponseLine, string, error) {
	if err := s.applyCommandDeadline(); err != nil {
		return nil, "", err
	}

	tag := fmt.Sprintf("A%04d", s.nextTag)
	s.nextTag++

	command := fmt.Sprintf(format, args...)
	if _, err := fmt.Fprintf(s.writer, "%s %s\r\n", tag, command); err != nil {
		return nil, "", fmt.Errorf("write %s command: %w", tag, err)
	}
	if err := s.writer.Flush(); err != nil {
		return nil, "", fmt.Errorf("flush %s command: %w", tag, err)
	}

	return s.readTaggedResponse(tag)
}

func (s *session) expectGreeting() error {
	if err := s.applyCommandDeadline(); err != nil {
		return err
	}

	line, err := s.readLine()
	if err != nil {
		return fmt.Errorf("read server greeting: %w", err)
	}
	if !strings.HasPrefix(strings.ToUpper(line), "* OK") {
		return fmt.Errorf("unexpected server greeting: %s", line)
	}

	return nil
}

func (s *session) applyCommandDeadline() error {
	if s == nil || s.conn == nil {
		return nil
	}
	if s.commandTimeout <= 0 {
		return nil
	}
	if err := s.conn.SetDeadline(time.Now().Add(s.commandTimeout)); err != nil {
		return fmt.Errorf("set connection deadline: %w", err)
	}

	return nil
}

func (s *session) readTaggedResponse(tag string) ([]imapResponseLine, string, error) {
	var responseLines []imapResponseLine

	for {
		line, err := s.readLine()
		if err != nil {
			return nil, "", fmt.Errorf("read %s response: %w", tag, err)
		}

		responseLine := imapResponseLine{line: line}
		if literalSize, ok := parseLiteralSize(line); ok {
			literal, err := s.readLiteral(literalSize)
			if err != nil {
				return nil, "", fmt.Errorf("read %s literal: %w", tag, err)
			}
			responseLine.literal = literal
		}

		if !strings.HasPrefix(line, tag+" ") {
			responseLines = append(responseLines, responseLine)
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, "", fmt.Errorf("malformed %s response: %s", tag, line)
		}

		switch strings.ToUpper(fields[1]) {
		case "OK":
			return responseLines, line, nil
		case "NO", "BAD":
			return responseLines, "", errors.New(line)
		default:
			return responseLines, "", fmt.Errorf("unexpected %s response: %s", tag, line)
		}
	}
}

func (s *session) readLine() (string, error) {
	line, err := s.reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	return strings.TrimRight(line, "\r\n"), nil
}

func quoteIMAPString(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("value cannot contain a newline")
	}

	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`, nil
}

func parseFetchSummary(mailbox string, responseLine imapResponseLine) (MessageSummary, error) {
	fields := strings.Fields(responseLine.line)
	if len(fields) < 3 {
		return MessageSummary{}, fmt.Errorf("malformed FETCH response: %s", responseLine.line)
	}

	sequenceNumber, err := strconv.Atoi(fields[1])
	if err != nil {
		return MessageSummary{}, fmt.Errorf("parse FETCH sequence number %q: %w", fields[1], err)
	}

	uid, err := extractUint32Token(responseLine.line, "UID ")
	if err != nil {
		return MessageSummary{}, err
	}

	internalDateValue, err := extractQuotedToken(responseLine.line, `INTERNALDATE "`)
	if err != nil {
		return MessageSummary{}, err
	}

	receivedAt, err := time.Parse("02-Jan-2006 15:04:05 -0700", internalDateValue)
	if err != nil {
		return MessageSummary{}, fmt.Errorf("parse INTERNALDATE %q: %w", internalDateValue, err)
	}
	flagged := parseFlaggedFromFetchLine(responseLine.line)

	headers, err := mail.ReadMessage(bytes.NewReader(responseLine.literal))
	if err != nil {
		return MessageSummary{}, fmt.Errorf("parse message headers: %w", err)
	}

	return MessageSummary{
		Mailbox:        mailbox,
		MessageID:      normalizeMessageID(headers.Header.Get("Message-Id")),
		SequenceNumber: sequenceNumber,
		UID:            uid,
		Flagged:        flagged,
		ReceivedAt:     receivedAt,
		Subject:        headers.Header.Get("Subject"),
		From:           headers.Header.Get("From"),
		To:             headers.Header.Get("To"),
	}, nil
}

func parseFlaggedFromFetchLine(line string) bool {
	upper := strings.ToUpper(line)
	flagsIndex := strings.Index(upper, "FLAGS (")
	if flagsIndex == -1 {
		return false
	}

	start := flagsIndex + len("FLAGS (")
	rest := upper[start:]
	end := strings.IndexByte(rest, ')')
	if end == -1 {
		return false
	}

	return strings.Contains(rest[:end], `\FLAGGED`)
}

func extractUint32Token(line, prefix string) (uint32, error) {
	start := strings.Index(line, prefix)
	if start == -1 {
		return 0, fmt.Errorf("missing %s token in response: %s", strings.TrimSpace(prefix), line)
	}

	start += len(prefix)
	end := start
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}

	value, err := strconv.ParseUint(line[start:end], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse %s value: %w", strings.TrimSpace(prefix), err)
	}

	return uint32(value), nil
}

func extractQuotedToken(line, prefix string) (string, error) {
	start := strings.Index(line, prefix)
	if start == -1 {
		return "", fmt.Errorf("missing %s token in response: %s", strings.TrimSpace(prefix), line)
	}

	start += len(prefix)
	end := start
	for end < len(line) && line[end] != '"' {
		end++
	}
	if end >= len(line) {
		return "", fmt.Errorf("unterminated quoted token in response: %s", line)
	}

	return line[start:end], nil
}

func parseLiteralSize(line string) (int, bool) {
	start := strings.LastIndex(line, "{")
	if start == -1 || !strings.HasSuffix(line, "}") {
		return 0, false
	}

	size, err := strconv.Atoi(line[start+1 : len(line)-1])
	if err != nil {
		return 0, false
	}

	return size, true
}

func (s *session) readLiteral(size int) ([]byte, error) {
	literal := make([]byte, size)
	if _, err := io.ReadFull(s.reader, literal); err != nil {
		return nil, err
	}

	return literal, nil
}

func formatIMAPDate(value time.Time) string {
	return value.Format("02-Jan-2006")
}

func startOfDay(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func parseListMailbox(line string) (string, bool, error) {
	remaining := strings.TrimSpace(strings.TrimPrefix(line, "* LIST"))
	if !strings.HasPrefix(remaining, "(") {
		return "", false, fmt.Errorf("malformed LIST response: %s", line)
	}

	flagsEnd := strings.Index(remaining, ")")
	if flagsEnd == -1 {
		return "", false, fmt.Errorf("malformed LIST flags: %s", line)
	}

	flags := strings.ToUpper(remaining[1:flagsEnd])
	remaining = strings.TrimSpace(remaining[flagsEnd+1:])

	_, consumed, err := consumeIMAPListToken(remaining)
	if err != nil {
		return "", false, fmt.Errorf("parse LIST delimiter: %w", err)
	}
	remaining = strings.TrimSpace(remaining[consumed:])

	mailbox, _, err := consumeIMAPListToken(remaining)
	if err != nil {
		return "", false, fmt.Errorf("parse LIST mailbox: %w", err)
	}

	return mailbox, !strings.Contains(flags, `\NOSELECT`), nil
}

func consumeIMAPListToken(input string) (string, int, error) {
	if input == "" {
		return "", 0, fmt.Errorf("missing token")
	}

	if input[0] == '"' {
		value, consumed, err := consumeQuotedString(input)
		if err != nil {
			return "", 0, err
		}

		return value, consumed, nil
	}

	tokenEnd := strings.IndexAny(input, " \t")
	if tokenEnd == -1 {
		return input, len(input), nil
	}

	return input[:tokenEnd], tokenEnd, nil
}

func consumeQuotedString(input string) (string, int, error) {
	var builder strings.Builder
	escaped := false

	for i := 1; i < len(input); i++ {
		character := input[i]
		switch {
		case escaped:
			builder.WriteByte(character)
			escaped = false
		case character == '\\':
			escaped = true
		case character == '"':
			return builder.String(), i + 1, nil
		default:
			builder.WriteByte(character)
		}
	}

	return "", 0, fmt.Errorf("unterminated quoted string: %s", input)
}

func dedupeMessageSummaries(summaries []MessageSummary) []MessageSummary {
	seenMessageIDs := make(map[string]struct{}, len(summaries))
	deduped := make([]MessageSummary, 0, len(summaries))

	for _, summary := range summaries {
		messageID := normalizeMessageID(summary.MessageID)
		if messageID == "" {
			deduped = append(deduped, summary)
			continue
		}

		if _, exists := seenMessageIDs[messageID]; exists {
			continue
		}

		seenMessageIDs[messageID] = struct{}{}
		deduped = append(deduped, summary)
	}

	return deduped
}

func normalizeMessageID(messageID string) string {
	return strings.TrimSpace(messageID)
}

func prioritizeDeleteMailboxes(mailboxes []string, isGmailAccount bool) []string {
	prioritized := append([]string(nil), mailboxes...)
	sort.SliceStable(prioritized, func(i, j int) bool {
		leftPriority := deleteMailboxPriority(prioritized[i], isGmailAccount)
		rightPriority := deleteMailboxPriority(prioritized[j], isGmailAccount)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}

		return strings.ToLower(prioritized[i]) < strings.ToLower(prioritized[j])
	})

	return prioritized
}

func deleteMailboxPriority(mailbox string, isGmailAccount bool) int {
	lowerMailbox := strings.ToLower(mailbox)

	switch {
	case isGmailAccount && isAllMailMailbox(lowerMailbox):
		return 0
	default:
		return mailboxPriority(mailbox) + 1
	}
}

func (s *session) isGmailAccount() bool {
	if s == nil || s.client == nil {
		return false
	}

	return s.client.isGmail()
}

func (c *Client) isGmail() bool {
	if c == nil {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(c.config.Provider)) {
	case "gmail", "googlemail":
		return true
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(c.config.Address))
	if err != nil {
		return false
	}

	return strings.EqualFold(host, "imap.gmail.com")
}

func (c *Client) isTaggedGmail() bool {
	if c == nil {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(c.config.Provider)) {
	case "gmail", "googlemail":
		return true
	default:
		return false
	}
}

func mailboxPriority(mailbox string) int {
	lowerMailbox := strings.ToLower(mailbox)

	switch {
	case lowerMailbox == "inbox" || strings.HasSuffix(lowerMailbox, "/inbox"):
		return 0
	case isArchiveMailbox(lowerMailbox):
		return 1
	case isAllMailMailbox(lowerMailbox):
		return 3
	case isSpamMailbox(lowerMailbox), isTrashMailbox(lowerMailbox):
		return 2
	default:
		return 1
	}
}

func isAllMailMailbox(mailbox string) bool {
	return strings.Contains(mailbox, "all mail")
}

func (s *session) shouldMoveAllMailToTrash(mailbox string) bool {
	if s == nil || s.client == nil {
		return false
	}
	if !s.client.isTaggedGmail() {
		return false
	}

	return isAllMailMailbox(strings.ToLower(mailbox))
}

func isArchiveMailbox(mailbox string) bool {
	return strings.Contains(mailbox, "archive")
}

func isSpamMailbox(mailbox string) bool {
	return strings.Contains(mailbox, "spam") || strings.Contains(mailbox, "junk")
}

func isTrashMailbox(mailbox string) bool {
	return strings.Contains(mailbox, "trash") || strings.Contains(mailbox, "bin")
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return strings.Contains(strings.ToLower(err.Error()), "i/o timeout")
}

func isRetryableConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if isTimeoutError(err) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EPIPE) {
		return true
	}

	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "connection reset by peer") ||
		strings.Contains(lower, "use of closed network connection")
}

func isUnsupportedMoveError(err error) bool {
	if err == nil {
		return false
	}

	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "unsupported uid command") ||
		strings.Contains(lower, "unknown command") ||
		strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "does not support")
}

func isDNSLookupError(err error) bool {
	if err == nil {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	return strings.Contains(strings.ToLower(err.Error()), "no such host")
}

func (s *session) reconnect() error {
	if s == nil || s.client == nil {
		return fmt.Errorf("client is required for reconnect")
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}

	ctx := context.Background()
	cancel := func() {}
	if s.commandTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.commandTimeout)
	}
	defer cancel()

	reconnected, err := s.client.login(ctx)
	if err != nil {
		return err
	}

	*s = *reconnected
	return nil
}
