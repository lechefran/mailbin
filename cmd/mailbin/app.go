package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lechefran/mailbin"
)

func newAppFromFlags() (*mailbin.App, error) {
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
	timeout := flag.Duration("timeout", 30*time.Second, "connection timeout")
	flag.Parse()

	if *age < 0 {
		return nil, fmt.Errorf("age is required and must be 0 or greater")
	}

	if *configPath == "" {
		password, err := resolvePassword(os.Stdin, os.Stderr, os.Getenv, stdinIsInteractive())
		if err != nil {
			return nil, err
		}
		addressValue, err := mailbin.ResolveIMAPAddress(*provider, *address)
		if err != nil {
			return nil, err
		}

		client := &mailbin.IMAPClient{
			Provider: *provider,
			Address:  addressValue,
			Email:    *email,
			Password: password,
		}

		return &mailbin.App{
			Client:         client,
			Age:            *age,
			IncludeFlagged: *includeFlagged,
			Timeout:        *timeout,
			Now:            time.Now,
		}, nil
	}

	accounts, err := loadConfiguredAccounts(*configPath, *accountName, os.Stdin, os.Stderr, os.Getenv, stdinIsInteractive())
	if err != nil {
		return nil, err
	}

	return &mailbin.App{
		Accounts:       accounts,
		Age:            *age,
		IncludeFlagged: *includeFlagged,
		Timeout:        *timeout,
		Now:            time.Now,
	}, nil
}

func runCLI(ctx context.Context, output io.Writer) error {
	app, err := newAppFromFlags()
	if err != nil {
		return err
	}

	result, err := app.Run(ctx)
	if result != nil {
		if writeErr := writeDeleteOutput(output, result); writeErr != nil {
			return writeErr
		}
	}

	return err
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

func writeDeleteOutput(output io.Writer, result *mailbin.RunResult) error {
	if result == nil {
		return nil
	}
	if err := writeEmailSummaries(output, result.Emails); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "deleted %d emails\n", len(result.Emails)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(
		output,
		"summary: deleted total=%d emails across accounts=%d (successful=%d failed=%d)\n",
		len(result.Emails),
		result.TotalAccounts,
		result.SuccessfulAccounts(),
		len(result.Failures),
	)
	return err
}

func writeEmailSummaries(output io.Writer, emails []mailbin.EmailSummary) error {
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
