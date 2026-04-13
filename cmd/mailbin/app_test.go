package main

import (
	"bytes"
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
	result := &mailbin.RunResult{
		Emails: []mailbin.EmailSummary{
			{
				Account:    "gmail",
				Mailbox:    "INBOX",
				ReceivedAt: time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC),
				Subject:    "Today message",
				From:       "alerts@example.com",
				To:         "user@example.com",
				UID:        7,
			},
		},
		TotalAccounts: 1,
	}

	if err := writeDeleteOutput(buffer, result); err != nil {
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
