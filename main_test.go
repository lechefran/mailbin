package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestAppReadByRange(t *testing.T) {
	now := time.Date(2026, time.April, 1, 15, 30, 0, 0, time.UTC)
	expected := []EmailSummary{
		{UID: 1, Subject: "sample"},
	}

	testCases := []struct {
		name          string
		appRange      string
		wantMethod    string
		wantErrorText string
	}{
		{name: "default all", appRange: "", wantMethod: "all"},
		{name: "all", appRange: "all", wantMethod: "all"},
		{name: "today", appRange: "today", wantMethod: "today"},
		{name: "week", appRange: "week", wantMethod: "week"},
		{name: "month", appRange: "month", wantMethod: "month"},
		{name: "invalid", appRange: "year", wantErrorText: `invalid range "year"`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			reader := &stubInboxReader{
				emails: expected,
			}

			app := &App{
				Range: testCase.appRange,
				Now: func() time.Time {
					return now
				},
			}

			emails, err := app.readByRange(reader)
			if testCase.wantErrorText != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErrorText) {
					t.Fatalf("readByRange() error = %v, want %q", err, testCase.wantErrorText)
				}
				return
			}

			if err != nil {
				t.Fatalf("readByRange() error = %v", err)
			}
			if len(emails) != len(expected) || emails[0].UID != expected[0].UID {
				t.Fatalf("readByRange() emails = %v, want %v", emails, expected)
			}
			if reader.called != testCase.wantMethod {
				t.Fatalf("reader called %q, want %q", reader.called, testCase.wantMethod)
			}
			if (testCase.wantMethod == "today" || testCase.wantMethod == "week" || testCase.wantMethod == "month") && !reader.calledWith.Equal(now) {
				t.Fatalf("reader called with %v, want %v", reader.calledWith, now)
			}
		})
	}
}

func TestAppRunPrintsEmailsForRange(t *testing.T) {
	buffer := &bytes.Buffer{}
	client := &stubLoginReader{
		session: &stubSession{
			stubInboxReader: stubInboxReader{
				emails: []EmailSummary{
					{
						UID:        7,
						ReceivedAt: time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC),
						Subject:    "Today message",
						From:       "alerts@example.com",
						To:         "user@example.com",
					},
				},
			},
		},
		Address: "imap.example.com:993",
		Email:   "user@example.com",
	}

	app := &App{
		Client: &IMAPClient{
			Address: client.Address,
			Email:   client.Email,
		},
		Login: func(context.Context) (SessionWithInboxRead, error) {
			return client.session, nil
		},
		Range:   "today",
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
		t.Fatalf("Run() output = %q, want subject", output)
	}
	if client.session.called != "today" {
		t.Fatalf("session called %q, want today", client.session.called)
	}
	if !client.session.loggedOut {
		t.Fatal("session was not logged out")
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

type stubLoginReader struct {
	session *stubSession
	Address string
	Email   string
}

type stubInboxReader struct {
	called     string
	calledWith time.Time
	emails     []EmailSummary
}

func (s *stubInboxReader) ReadInboxAll() ([]EmailSummary, error) {
	s.called = "all"
	return s.emails, nil
}

func (s *stubInboxReader) ReadInboxToday(now time.Time) ([]EmailSummary, error) {
	s.called = "today"
	s.calledWith = now
	return s.emails, nil
}

func (s *stubInboxReader) ReadInboxThisWeek(now time.Time) ([]EmailSummary, error) {
	s.called = "week"
	s.calledWith = now
	return s.emails, nil
}

func (s *stubInboxReader) ReadInboxThisMonth(now time.Time) ([]EmailSummary, error) {
	s.called = "month"
	s.calledWith = now
	return s.emails, nil
}

type stubSession struct {
	stubInboxReader
	loggedOut bool
}

func (s *stubSession) Logout() error {
	s.loggedOut = true
	return nil
}
