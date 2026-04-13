package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultAccountTimeout = 30 * time.Second

type App struct {
	Client         *IMAPClient
	Accounts       []ConfiguredAccount
	Login          func(context.Context, *IMAPClient) (DeleteSession, error)
	Age            int
	IncludeFlagged bool
	Concurrency    int
	Timeout        time.Duration
	Now            func() time.Time
	Output         io.Writer
	PrintEmails    func(io.Writer, []EmailSummary) error
}

type ConfiguredAccount struct {
	Name   string
	Client *IMAPClient
}

type DeleteSession interface {
	DeleteInboxOlderThanDays(time.Time, int, bool) ([]EmailSummary, error)
	Logout() error
}

func (a *App) Run(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("app is required")
	}

	accounts, err := a.resolveAccounts()
	if err != nil {
		return err
	}

	timeout := a.Timeout
	if timeout <= 0 {
		timeout = defaultAccountTimeout
	}

	login := a.Login
	if login == nil {
		login = func(ctx context.Context, client *IMAPClient) (DeleteSession, error) {
			return client.Login(ctx)
		}
	}

	output := a.Output
	if output == nil {
		output = os.Stdout
	}

	printEmails := a.PrintEmails
	if printEmails == nil {
		printEmails = writeEmailSummaries
	}

	type accountRunResult struct {
		accountName string
		emails      []EmailSummary
		err         error
	}

	results := make(chan accountRunResult, len(accounts))
	var sem chan struct{}
	if a.Concurrency > 0 {
		sem = make(chan struct{}, a.Concurrency)
	}
	var wg sync.WaitGroup
	for _, account := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() {
					<-sem
				}()
			}

			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			log.Printf("starting account %s delete", account.Name)
			session, err := login(runCtx, account.Client)
			if err != nil {
				results <- accountRunResult{
					accountName: account.Name,
					err:         err,
				}
				return
			}

			log.Printf("connected to IMAP server %s as %s", account.Client.Address, account.Client.Email)

			emails, err := a.deleteByAge(session)
			logoutErr := session.Logout()
			if len(accounts) > 1 {
				for index := range emails {
					emails[index].Account = account.Name
				}
			}
			if err != nil {
				results <- accountRunResult{
					accountName: account.Name,
					emails:      emails,
					err:         err,
				}
				return
			}
			if logoutErr != nil {
				if isTimeoutError(logoutErr) {
					log.Printf("ignoring logout timeout for account %s: %v", account.Name, logoutErr)
					logoutErr = nil
				}
			}
			if logoutErr != nil {
				results <- accountRunResult{
					accountName: account.Name,
					emails:      emails,
					err:         logoutErr,
				}
				return
			}

			log.Printf("finished deletion for account %s: deleted %d emails", account.Name, len(emails))

			results <- accountRunResult{
				accountName: account.Name,
				emails:      emails,
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var allEmails []EmailSummary
	var failures []string
	for result := range results {
		allEmails = append(allEmails, result.emails...)
		if result.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", result.accountName, result.err))
			continue
		}
	}

	if len(allEmails) == 0 && len(failures) > 0 {
		return fmt.Errorf("%d account(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}

	if err := printEmails(output, allEmails); err != nil {
		return err
	}
	if err := writeActionSummary(output, len(allEmails)); err != nil {
		return err
	}
	if err := writeCrossAccountSummary(output, len(accounts), len(failures), len(allEmails)); err != nil {
		return err
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d account(s) failed: %s", len(failures), strings.Join(failures, "; "))
	}

	return nil
}

func main() {
	app, err := newAppFromFlags()
	if err != nil {
		log.Fatal(err)
	}

	if err := app.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func newAppFromFlags() (*App, error) {
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
	includeFlagged := flag.Bool(
		"include-flagged",
		envBoolOrDefault("MAILBIN_INCLUDE_FLAGGED", false),
		"deprecated: flagged/starred emails are never deleted",
	)
	timeout := flag.Duration("timeout", defaultAccountTimeout, "connection timeout")
	flag.Parse()

	if *age < 0 {
		return nil, fmt.Errorf("age is required and must be 0 or greater")
	}

	if *configPath == "" {
		password, err := resolvePassword(os.Stdin, os.Stderr, os.Getenv, stdinIsInteractive())
		if err != nil {
			return nil, err
		}
		addressValue, err := resolveIMAPAddress(*provider, *address)
		if err != nil {
			return nil, err
		}

		client := &IMAPClient{
			Provider: *provider,
			Address:  addressValue,
			Email:    *email,
			Password: password,
		}

		return &App{
			Client:         client,
			Age:            *age,
			IncludeFlagged: *includeFlagged,
			Timeout:        *timeout,
			Now:            time.Now,
			Output:         os.Stdout,
		}, nil
	}

	accounts, err := loadConfiguredAccounts(*configPath, *accountName, os.Stdin, os.Stderr, os.Getenv, stdinIsInteractive())
	if err != nil {
		return nil, err
	}

	return &App{
		Accounts:       accounts,
		Age:            *age,
		IncludeFlagged: *includeFlagged,
		Timeout:        *timeout,
		Now:            time.Now,
		Output:         os.Stdout,
	}, nil
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

func (a *App) resolveAccounts() ([]ConfiguredAccount, error) {
	if len(a.Accounts) > 0 {
		return a.Accounts, nil
	}
	if a.Client != nil {
		return []ConfiguredAccount{
			{
				Name:   defaultAccountName(a.Client.Email),
				Client: a.Client,
			},
		}, nil
	}

	return nil, fmt.Errorf("at least one account is required")
}

func (a *App) deleteByAge(session DeleteSession) ([]EmailSummary, error) {
	if a.Age < 0 {
		return nil, fmt.Errorf("age is required and must be 0 or greater")
	}

	now := time.Now
	if a.Now != nil {
		now = a.Now
	}

	return session.DeleteInboxOlderThanDays(now(), a.Age, a.IncludeFlagged)
}

func writeEmailSummaries(output io.Writer, emails []EmailSummary) error {
	for _, email := range emails {
		accountPrefix := ""
		if email.Account != "" {
			accountPrefix = fmt.Sprintf("account=%s | ", email.Account)
		}
		receivedAt := "unknown-time"
		if !email.ReceivedAt.IsZero() {
			receivedAt = email.ReceivedAt.Format(time.RFC3339)
		}
		subject := email.Subject
		if subject == "" {
			subject = "-"
		}
		from := email.From
		if from == "" {
			from = "-"
		}
		to := email.To
		if to == "" {
			to = "-"
		}
		if _, err := fmt.Fprintf(
			output,
			"%s | %smailbox=%s | %s | from=%s | to=%s | uid=%d\n",
			receivedAt,
			accountPrefix,
			email.Mailbox,
			subject,
			from,
			to,
			email.UID,
		); err != nil {
			return err
		}
	}

	return nil
}

func writeActionSummary(output io.Writer, count int) error {
	_, err := fmt.Fprintf(output, "deleted %d emails\n", count)
	return err
}

func writeCrossAccountSummary(output io.Writer, totalAccounts, failedAccounts, totalEmails int) error {
	successfulAccounts := totalAccounts - failedAccounts
	_, err := fmt.Fprintf(
		output,
		"summary: deleted total=%d emails across accounts=%d (successful=%d failed=%d)\n",
		totalEmails,
		totalAccounts,
		successfulAccounts,
		failedAccounts,
	)
	return err
}

func defaultAccountName(email string) string {
	if email == "" {
		return "account"
	}

	return email
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

func envBoolOrDefault(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}

	return parsed
}
