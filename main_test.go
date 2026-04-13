package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAppDeleteByAge(t *testing.T) {
	now := time.Date(2026, time.April, 1, 15, 30, 0, 0, time.UTC)
	expected := []EmailSummary{{UID: 1, Subject: "sample"}}
	session := &stubDeleteSession{
		emails: expected,
	}

	app := &App{
		Age:            90,
		IncludeFlagged: true,
		Now: func() time.Time {
			return now
		},
	}

	emails, err := app.deleteByAge(session)
	if err != nil {
		t.Fatalf("deleteByAge() error = %v", err)
	}
	if len(emails) != 1 || emails[0].UID != expected[0].UID {
		t.Fatalf("deleteByAge() emails = %v, want %v", emails, expected)
	}
	if session.calledAge != 90 {
		t.Fatalf("deleteByAge() age = %d, want 90", session.calledAge)
	}
	if !session.calledIncludeFlagged {
		t.Fatal("deleteByAge() includeFlagged = false, want true")
	}
	if !session.calledWith.Equal(now) {
		t.Fatalf("deleteByAge() calledWith = %v, want %v", session.calledWith, now)
	}
}

func TestAppDeleteByAgeRequiresAge(t *testing.T) {
	app := &App{Age: -1}

	_, err := app.deleteByAge(&stubDeleteSession{})
	if err == nil || !strings.Contains(err.Error(), "age is required") {
		t.Fatalf("deleteByAge() error = %v, want age validation error", err)
	}
}

func TestAppRunDeletePrintsEmailsAndCount(t *testing.T) {
	buffer := &bytes.Buffer{}
	session := &stubDeleteSession{
		emails: []EmailSummary{
			{
				UID:        7,
				Mailbox:    "INBOX",
				ReceivedAt: time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC),
				Subject:    "Today message",
				From:       "alerts@example.com",
				To:         "user@example.com",
			},
			{
				UID:        8,
				Mailbox:    "[Gmail]/Spam",
				ReceivedAt: time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC),
				Subject:    "Spam message",
				From:       "spam@example.com",
				To:         "user@example.com",
			},
		},
	}

	app := &App{
		Client: &IMAPClient{
			Address: "imap.example.com:993",
			Email:   "user@example.com",
		},
		Login: func(context.Context, *IMAPClient) (DeleteSession, error) {
			return session, nil
		},
		Age:     0,
		Timeout: time.Second,
		Now: func() time.Time {
			return time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC)
		},
		Output: buffer,
	}

	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := buffer.String()
	if !strings.Contains(output, "Today message") {
		t.Fatalf("Run() output = %q, want first subject", output)
	}
	if !strings.Contains(output, "Spam message") {
		t.Fatalf("Run() output = %q, want second subject", output)
	}
	if !strings.Contains(output, "deleted 2 emails") {
		t.Fatalf("Run() output = %q, want deleted count", output)
	}
	if session.calledAge != 0 {
		t.Fatalf("Run() age = %d, want 0", session.calledAge)
	}
	if !session.loggedOut {
		t.Fatal("Run() session was not logged out")
	}
}

func TestAppRunMultipleAccountsAggregatesOutput(t *testing.T) {
	buffer := &bytes.Buffer{}
	sessions := map[string]*stubDeleteSession{
		"one@example.com": {
			emails: []EmailSummary{{UID: 1, Subject: "First account message"}},
		},
		"two@example.com": {
			emails: []EmailSummary{{UID: 2, Subject: "Second account message"}},
		},
	}

	app := &App{
		Accounts: []ConfiguredAccount{
			{
				Name: "gmail",
				Client: &IMAPClient{
					Address: "imap.gmail.com:993",
					Email:   "one@example.com",
				},
			},
			{
				Name: "icloud",
				Client: &IMAPClient{
					Address: "imap.mail.me.com:993",
					Email:   "two@example.com",
				},
			},
		},
		Login: func(_ context.Context, client *IMAPClient) (DeleteSession, error) {
			return sessions[client.Email], nil
		},
		Age:     90,
		Timeout: time.Second,
		Output:  buffer,
	}

	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := buffer.String()
	if !strings.Contains(output, "account=gmail |") {
		t.Fatalf("Run() output = %q, want gmail account label", output)
	}
	if !strings.Contains(output, "account=icloud |") {
		t.Fatalf("Run() output = %q, want icloud account label", output)
	}
	if strings.Count(output, "deleted 2 emails") != 1 {
		t.Fatalf("Run() output = %q, want one aggregated summary", output)
	}
	if !strings.Contains(output, "summary: deleted total=2 emails across accounts=2 (successful=2 failed=0)") {
		t.Fatalf("Run() output = %q, want cross-account delete summary", output)
	}
	if !sessions["one@example.com"].loggedOut || !sessions["two@example.com"].loggedOut {
		t.Fatalf("sessions logged out = %#v", sessions)
	}
}

func TestAppRunDeleteMultipleAccountsRunsConcurrentlyAndAggregatesCount(t *testing.T) {
	buffer := &bytes.Buffer{}
	started := make(chan string, 2)
	release := make(chan struct{})
	errs := make(chan error, 1)

	app := &App{
		Accounts: []ConfiguredAccount{
			{
				Name: "gmail",
				Client: &IMAPClient{
					Address: "imap.gmail.com:993",
					Email:   "one@example.com",
				},
			},
			{
				Name: "icloud",
				Client: &IMAPClient{
					Address: "imap.mail.me.com:993",
					Email:   "two@example.com",
				},
			},
		},
		Login: func(_ context.Context, client *IMAPClient) (DeleteSession, error) {
			return &blockingDeleteSession{
				email:   client.Email,
				started: started,
				release: release,
				emails: []EmailSummary{
					{
						UID:     1,
						Mailbox: "INBOX",
						Subject: client.Email,
					},
				},
			}, nil
		},
		Age:         90,
		Concurrency: 2,
		Timeout:     time.Second,
		Output:      buffer,
	}

	go func() {
		errs <- app.Run(context.Background())
	}()

	first := <-started
	second := <-started
	if first == second {
		t.Fatalf("started accounts = %q and %q, want distinct concurrent deletes", first, second)
	}
	close(release)

	if err := <-errs; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := buffer.String()
	if !strings.Contains(output, "account=gmail |") {
		t.Fatalf("Run() output = %q, want gmail account label", output)
	}
	if !strings.Contains(output, "account=icloud |") {
		t.Fatalf("Run() output = %q, want icloud account label", output)
	}
	if !strings.Contains(output, "deleted 2 emails") {
		t.Fatalf("Run() output = %q, want aggregated deleted count", output)
	}
	if !strings.Contains(output, "summary: deleted total=2 emails across accounts=2 (successful=2 failed=0)") {
		t.Fatalf("Run() output = %q, want cross-account delete summary", output)
	}
}

func TestAppRunDeleteHonorsConcurrencyLimit(t *testing.T) {
	buffer := &bytes.Buffer{}
	started := make(chan string, 2)
	release := make(chan struct{})
	errs := make(chan error, 1)

	app := &App{
		Accounts: []ConfiguredAccount{
			{
				Name: "gmail",
				Client: &IMAPClient{
					Address: "imap.gmail.com:993",
					Email:   "one@example.com",
				},
			},
			{
				Name: "icloud",
				Client: &IMAPClient{
					Address: "imap.mail.me.com:993",
					Email:   "two@example.com",
				},
			},
		},
		Login: func(_ context.Context, client *IMAPClient) (DeleteSession, error) {
			return &blockingDeleteSession{
				email:   client.Email,
				started: started,
				release: release,
				emails: []EmailSummary{
					{
						UID:     1,
						Mailbox: "INBOX",
						Subject: client.Email,
					},
				},
			}, nil
		},
		Age:         90,
		Concurrency: 1,
		Timeout:     time.Second,
		Output:      buffer,
	}

	go func() {
		errs <- app.Run(context.Background())
	}()

	first := <-started
	select {
	case second := <-started:
		t.Fatalf("started accounts = %q and %q, want only one account to start before release", first, second)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	second := <-started
	if first == second {
		t.Fatalf("started accounts = %q and %q, want distinct accounts", first, second)
	}

	if err := <-errs; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestAppRunDoesNotPrintEmptySummaryWhenAllAccountsFail(t *testing.T) {
	buffer := &bytes.Buffer{}

	app := &App{
		Accounts: []ConfiguredAccount{
			{
				Name: "gmail",
				Client: &IMAPClient{
					Address: "imap.gmail.com:993",
					Email:   "one@example.com",
				},
			},
			{
				Name: "icloud",
				Client: &IMAPClient{
					Address: "imap.mail.me.com:993",
					Email:   "two@example.com",
				},
			},
		},
		Login: func(_ context.Context, client *IMAPClient) (DeleteSession, error) {
			return nil, fmt.Errorf("login failed for %s", client.Email)
		},
		Age:     90,
		Timeout: time.Second,
		Output:  buffer,
	}

	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "2 account(s) failed") {
		t.Fatalf("Run() error = %v, want aggregated failure", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("Run() output = %q, want no empty summary", buffer.String())
	}
}

func TestAppRunIgnoresLogoutTimeoutAfterSuccessfulDelete(t *testing.T) {
	buffer := &bytes.Buffer{}
	app := &App{
		Client: &IMAPClient{
			Address: "imap.example.com:993",
			Email:   "user@example.com",
		},
		Login: func(context.Context, *IMAPClient) (DeleteSession, error) {
			return &timeoutLogoutSession{
				stubDeleteSession: stubDeleteSession{
					emails: []EmailSummary{
						{UID: 1, Subject: "Old message"},
					},
				},
			}, nil
		},
		Age:     90,
		Timeout: time.Second,
		Output:  buffer,
	}

	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := buffer.String()
	if !strings.Contains(output, "Old message") {
		t.Fatalf("Run() output = %q, want deleted message", output)
	}
	if !strings.Contains(output, "deleted 1 emails") {
		t.Fatalf("Run() output = %q, want delete summary", output)
	}
}

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

type stubDeleteSession struct {
	calledWith           time.Time
	calledAge            int
	calledIncludeFlagged bool
	emails               []EmailSummary
	loggedOut            bool
}

func (s *stubDeleteSession) DeleteInboxOlderThanDays(now time.Time, age int, includeFlagged bool) ([]EmailSummary, error) {
	s.calledWith = now
	s.calledAge = age
	s.calledIncludeFlagged = includeFlagged
	return s.emails, nil
}

func (s *stubDeleteSession) Logout() error {
	s.loggedOut = true
	return nil
}

type timeoutLogoutSession struct {
	stubDeleteSession
}

func (s *timeoutLogoutSession) Logout() error {
	s.loggedOut = true
	return &net.DNSError{
		Err:         "i/o timeout",
		IsTimeout:   true,
		IsTemporary: true,
	}
}

type blockingDeleteSession struct {
	email     string
	started   chan<- string
	release   <-chan struct{}
	emails    []EmailSummary
	loggedOut bool
}

func (s *blockingDeleteSession) DeleteInboxOlderThanDays(time.Time, int, bool) ([]EmailSummary, error) {
	s.started <- s.email
	<-s.release
	return s.emails, nil
}

func (s *blockingDeleteSession) Logout() error {
	s.loggedOut = true
	return nil
}
