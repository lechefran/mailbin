package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lechefran/mailbin"
)

const defaultAccountTimeout = 30 * time.Second

type configuredAccount struct {
	Name   string
	Config mailbin.Config
}

type cliOptions struct {
	Accounts []configuredAccount
	Criteria mailbin.DeleteCriteria
	Timeout  time.Duration
}

type accountDeleteResult struct {
	AccountName string
	Deleted     []mailbin.MessageSummary
	Err         error
}

type indexedAccountDeleteResult struct {
	Index  int
	Result accountDeleteResult
}

type deleteFunc func(context.Context, mailbin.Config, mailbin.DeleteCriteria) (mailbin.DeleteResult, error)

func newCLIOptionsFromFlags(now time.Time) (*cliOptions, error) {
	configPath := flag.String("config", envOrDefault("MAILBIN_CONFIG", ""), "path to accounts config JSON")
	accountName := flag.String("account", envOrDefault("MAILBIN_ACCOUNT", ""), "account name from config to run")
	provider := flag.String("provider", envOrDefault("MAILBIN_PROVIDER", ""), "email provider name for built-in IMAP defaults")
	address := flag.String("imap-addr", envOrDefault("MAILBIN_IMAP_ADDR", ""), "IMAP server address in host:port format")
	email := flag.String("email", envOrDefault("MAILBIN_EMAIL", ""), "email address used for IMAP login")
	ageDefault, err := envIntOrDefault("MAILBIN_AGE", -1)
	if err != nil {
		return nil, err
	}
	age := flag.Int("age", ageDefault, "minimum email age in days to delete")
	timeout := flag.Duration("timeout", defaultAccountTimeout, "connection timeout")
	flag.Parse()

	if *age < 0 {
		return nil, fmt.Errorf("age is required and must be 0 or greater")
	}

	var accounts []configuredAccount
	if *configPath == "" {
		password, err := resolvePassword(os.Stdin, os.Stderr, os.Getenv, stdinIsInteractive())
		if err != nil {
			return nil, err
		}
		addressValue, err := mailbin.ResolveIMAPAddress(*provider, *address)
		if err != nil {
			return nil, err
		}

		accounts = []configuredAccount{
			{
				Name: defaultAccountName("", *email),
				Config: mailbin.Config{
					Provider: *provider,
					Address:  addressValue,
					Email:    *email,
					Password: password,
				},
			},
		}
	} else {
		accounts, err = loadConfiguredAccounts(*configPath, *accountName, os.Stdin, os.Stderr, os.Getenv, stdinIsInteractive())
		if err != nil {
			return nil, err
		}
	}

	return &cliOptions{
		Accounts: accounts,
		Criteria: mailbin.DeleteCriteria{
			ReceivedBefore: deleteCutoff(now, *age),
		},
		Timeout: *timeout,
	}, nil
}

func runCLI(ctx context.Context, output io.Writer) error {
	options, err := newCLIOptionsFromFlags(time.Now())
	if err != nil {
		return err
	}

	results, err := runConfiguredAccounts(ctx, options, deleteWithClient)
	if successfulAccountCount(results) > 0 {
		if writeErr := writeDeleteOutput(output, results); writeErr != nil {
			return writeErr
		}
	}

	return err
}

func runConfiguredAccounts(ctx context.Context, options *cliOptions, deleteAccount deleteFunc) ([]accountDeleteResult, error) {
	if options == nil {
		return nil, fmt.Errorf("cli options are required")
	}
	if len(options.Accounts) == 0 {
		return nil, fmt.Errorf("at least one account is required")
	}
	if deleteAccount == nil {
		deleteAccount = deleteWithClient
	}

	results := make(chan indexedAccountDeleteResult, len(options.Accounts))
	var wg sync.WaitGroup
	for index, account := range options.Accounts {
		index := index
		account := account
		wg.Add(1)
		go func() {
			defer wg.Done()

			runCtx, cancel := context.WithTimeout(ctx, options.Timeout)
			defer cancel()

			deleteResult, err := deleteAccount(runCtx, account.Config, options.Criteria)
			results <- indexedAccountDeleteResult{
				Index: index,
				Result: accountDeleteResult{
					AccountName: account.Name,
					Deleted:     deleteResult.Deleted,
					Err:         err,
				},
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	collected := make([]accountDeleteResult, len(options.Accounts))
	for result := range results {
		collected[result.Index] = result.Result
	}

	failures := make([]string, 0, len(collected))
	for _, result := range collected {
		if result.Err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", result.AccountName, result.Err))
		}
	}

	if len(failures) > 0 {
		return collected, fmt.Errorf("%d account(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}

	return collected, nil
}

func deleteWithClient(ctx context.Context, config mailbin.Config, criteria mailbin.DeleteCriteria) (mailbin.DeleteResult, error) {
	client, err := mailbin.NewClient(config)
	if err != nil {
		return mailbin.DeleteResult{}, err
	}

	return client.Delete(ctx, criteria)
}

func resolvePassword(input io.Reader, prompt io.Writer, getenv func(string) string, interactive bool) (string, error) {
	if password := getenv("MAILBIN_PASSWORD"); password != "" {
		return password, nil
	}

	if !interactive {
		return "", fmt.Errorf("MAILBIN_PASSWORD is required when stdin is not interactive")
	}

	return promptPassword(input, prompt, "Enter IMAP password: ")
}

func stdinIsInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

func writeDeleteOutput(output io.Writer, results []accountDeleteResult) error {
	if len(results) == 0 {
		return nil
	}

	totalDeleted := 0
	multipleAccounts := len(results) > 1
	for _, result := range results {
		totalDeleted += len(result.Deleted)
		if err := writeMessageSummaries(output, result.AccountName, multipleAccounts, result.Deleted); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintf(output, "deleted %d emails\n", totalDeleted); err != nil {
		return err
	}
	_, err := fmt.Fprintf(
		output,
		"summary: deleted total=%d emails across accounts=%d (successful=%d failed=%d)\n",
		totalDeleted,
		len(results),
		successfulAccountCount(results),
		failedAccountCount(results),
	)
	return err
}

func writeMessageSummaries(output io.Writer, accountName string, includeAccount bool, summaries []mailbin.MessageSummary) error {
	for _, summary := range summaries {
		accountPrefix := ""
		if includeAccount {
			accountPrefix = fmt.Sprintf("account=%s | ", accountName)
		}
		receivedAt := "unknown-time"
		if !summary.ReceivedAt.IsZero() {
			receivedAt = summary.ReceivedAt.Format(time.RFC3339)
		}
		subject := summary.Subject
		if subject == "" {
			subject = "-"
		}
		from := summary.From
		if from == "" {
			from = "-"
		}
		to := summary.To
		if to == "" {
			to = "-"
		}
		if _, err := fmt.Fprintf(
			output,
			"%s | %smailbox=%s | %s | from=%s | to=%s | uid=%d\n",
			receivedAt,
			accountPrefix,
			summary.Mailbox,
			subject,
			from,
			to,
			summary.UID,
		); err != nil {
			return err
		}
	}

	return nil
}

func successfulAccountCount(results []accountDeleteResult) int {
	successful := 0
	for _, result := range results {
		if result.Err == nil {
			successful++
		}
	}

	return successful
}

func failedAccountCount(results []accountDeleteResult) int {
	failed := 0
	for _, result := range results {
		if result.Err != nil {
			failed++
		}
	}

	return failed
}

func deleteCutoff(now time.Time, age int) time.Time {
	return startOfDay(now.AddDate(0, 0, -age)).AddDate(0, 0, 1)
}

func startOfDay(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

func envIntOrDefault(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}

	return parsed, nil
}
