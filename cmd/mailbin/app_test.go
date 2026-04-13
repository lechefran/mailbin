package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lechefran/mailbin"
)

func TestResolvePassword(t *testing.T) {
	testCases := []struct {
		name          string
		input         string
		envValue      string
		interactive   bool
		wantPassword  string
		wantPrompt    string
		wantErrorText string
	}{
		{
			name:         "uses env password",
			envValue:     "env-secret",
			interactive:  false,
			wantPassword: "env-secret",
		},
		{
			name:         "prompts on interactive stdin",
			input:        "typed-secret\n",
			interactive:  true,
			wantPassword: "typed-secret",
			wantPrompt:   "Enter IMAP password: ",
		},
		{
			name:          "errors on non interactive stdin",
			interactive:   false,
			wantErrorText: "MAILBIN_PASSWORD is required",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			prompt := &bytes.Buffer{}
			password, err := resolvePassword(
				strings.NewReader(testCase.input),
				prompt,
				func(string) string { return testCase.envValue },
				testCase.interactive,
			)

			if testCase.wantErrorText != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErrorText) {
					t.Fatalf("resolvePassword() error = %v, want %q", err, testCase.wantErrorText)
				}
				return
			}

			if err != nil {
				t.Fatalf("resolvePassword() error = %v", err)
			}
			if password != testCase.wantPassword {
				t.Fatalf("resolvePassword() password = %q, want %q", password, testCase.wantPassword)
			}
			if prompt.String() != testCase.wantPrompt {
				t.Fatalf("resolvePassword() prompt = %q, want %q", prompt.String(), testCase.wantPrompt)
			}
		})
	}
}

func TestWriteDeleteOutput(t *testing.T) {
	buffer := &bytes.Buffer{}
	results := []accountDeleteResult{
		{
			AccountName: "gmail",
			Deleted: []mailbin.MessageSummary{
				{
					Mailbox:    "INBOX",
					ReceivedAt: time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC),
					Subject:    "Today message",
					From:       "alerts@example.com",
					To:         "user@example.com",
					UID:        7,
				},
			},
		},
	}

	if err := writeDeleteOutput(buffer, results); err != nil {
		t.Fatalf("writeDeleteOutput() error = %v", err)
	}

	output := buffer.String()
	if !strings.Contains(output, "Today message") {
		t.Fatalf("writeDeleteOutput() output = %q, want subject", output)
	}
	if !strings.Contains(output, "deleted 1 emails") {
		t.Fatalf("writeDeleteOutput() output = %q, want count", output)
	}
	if !strings.Contains(output, "summary: deleted total=1 emails across accounts=1 (successful=1 failed=0)") {
		t.Fatalf("writeDeleteOutput() output = %q, want summary", output)
	}
}

func TestRunConfiguredAccountsRunsConcurrentlyAndPreservesInputOrder(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})

	options := &cliOptions{
		Accounts: []configuredAccount{
			{
				Name: "gmail",
				Config: mailbin.Config{
					Email: "one@example.com",
				},
			},
			{
				Name: "icloud",
				Config: mailbin.Config{
					Email: "two@example.com",
				},
			},
		},
		Criteria: mailbin.DeleteCriteria{ReceivedBefore: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)},
		Timeout:  time.Second,
	}

	done := make(chan struct{})
	var (
		results []accountDeleteResult
		err     error
	)
	go func() {
		results, err = runConfiguredAccounts(context.Background(), options, func(ctx context.Context, config mailbin.Config, criteria mailbin.DeleteCriteria) (mailbin.DeleteResult, error) {
			started <- config.Email
			<-release
			return mailbin.DeleteResult{
				Deleted: []mailbin.MessageSummary{
					{Subject: config.Email},
				},
			}, nil
		})
		close(done)
	}()

	first := <-started
	second := <-started
	if first == second {
		t.Fatalf("started accounts = %q and %q, want distinct concurrent deletes", first, second)
	}
	close(release)
	<-done

	if err != nil {
		t.Fatalf("runConfiguredAccounts() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("runConfiguredAccounts() results = %v, want 2", results)
	}
	if results[0].AccountName != "gmail" || results[1].AccountName != "icloud" {
		t.Fatalf("runConfiguredAccounts() account order = %#v, want input order", results)
	}
	if results[0].Deleted[0].Subject != "one@example.com" || results[1].Deleted[0].Subject != "two@example.com" {
		t.Fatalf("runConfiguredAccounts() deleted = %#v, want per-account results", results)
	}
}

func TestRunConfiguredAccountsAggregatesFailuresInInputOrder(t *testing.T) {
	options := &cliOptions{
		Accounts: []configuredAccount{
			{Name: "gmail", Config: mailbin.Config{Email: "one@example.com"}},
			{Name: "icloud", Config: mailbin.Config{Email: "two@example.com"}},
		},
		Criteria: mailbin.DeleteCriteria{ReceivedBefore: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)},
		Timeout:  time.Second,
	}

	results, err := runConfiguredAccounts(context.Background(), options, func(ctx context.Context, config mailbin.Config, criteria mailbin.DeleteCriteria) (mailbin.DeleteResult, error) {
		if config.Email == "one@example.com" {
			return mailbin.DeleteResult{}, errors.New("first failed")
		}
		return mailbin.DeleteResult{}, errors.New("second failed")
	})

	if err == nil {
		t.Fatal("runConfiguredAccounts() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "gmail: first failed; icloud: second failed") {
		t.Fatalf("runConfiguredAccounts() error = %v, want ordered failure summary", err)
	}
	if len(results) != 2 {
		t.Fatalf("runConfiguredAccounts() results = %v, want 2", results)
	}
}
