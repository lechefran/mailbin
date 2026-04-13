package mailbin

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewClientResolvesProviderDefaults(t *testing.T) {
	client, err := NewClient(Config{
		Provider: "gmail",
		Email:    "user@example.com",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.config.Address != GMAIL {
		t.Fatalf("NewClient() address = %q, want %q", client.config.Address, GMAIL)
	}
	if client.config.Email != "user@example.com" {
		t.Fatalf("NewClient() email = %q, want trimmed email", client.config.Email)
	}
}

func TestNewClientRequiresResolvableAddress(t *testing.T) {
	_, err := NewClient(Config{
		Email:    "user@example.com",
		Password: "secret",
	})
	if err == nil || !strings.Contains(err.Error(), "imap address or provider is required") {
		t.Fatalf("NewClient() error = %v, want address resolution failure", err)
	}
}

func TestClientDeleteRequiresReceivedBefore(t *testing.T) {
	client, err := NewClient(Config{
		Address:  "imap.example.com:993",
		Email:    "user@example.com",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Delete(context.Background(), DeleteCriteria{})
	if err == nil || !strings.Contains(err.Error(), "received before is required") {
		t.Fatalf("Delete() error = %v, want criteria validation failure", err)
	}
}

func TestClientDeleteDeletesMatchingMessages(t *testing.T) {
	server := newFakeIMAPServer(t, fakeIMAPServerConfig{
		email:    "user@example.com",
		password: "correct-password",
		accept:   true,
		mailboxes: []fakeIMAPMailbox{
			{
				Name: "INBOX",
				Messages: []fakeIMAPMessage{
					{
						UID:        101,
						MessageID:  "<old@example.com>",
						ReceivedAt: time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC),
						Subject:    "Old message",
						From:       "alerts@example.com",
						To:         "user@example.com",
					},
					{
						UID:        102,
						MessageID:  "<new@example.com>",
						ReceivedAt: time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC),
						Subject:    "New message",
						From:       "alerts@example.com",
						To:         "user@example.com",
					},
				},
			},
		},
	})
	t.Cleanup(server.Close)

	client, err := NewClient(Config{
		Address:   server.Address(),
		Email:     "user@example.com",
		Password:  "correct-password",
		TLSConfig: server.ClientTLSConfig(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.Delete(ctx, DeleteCriteria{
		ReceivedBefore: time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(result.Deleted) != 1 {
		t.Fatalf("Delete() deleted = %v, want 1 message", result.Deleted)
	}
	if result.Deleted[0].Subject != "Old message" {
		t.Fatalf("Delete() subject = %q, want old message", result.Deleted[0].Subject)
	}
}
