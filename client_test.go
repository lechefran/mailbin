package mailbin

import (
	"context"
	"errors"
	"testing"
)

func TestNewClientRequiresResolvableAddress(t *testing.T) {
	_, err := NewClient(Config{
		Email:    "user@example.com",
		Password: "secret",
	})
	if err == nil || !errors.Is(err, ErrIMAPAddressOrProviderRequired) {
		t.Fatalf("NewClient() error = %v, want address resolution failure", err)
	}
}

func TestNewClientAcceptsProviderDefaults(t *testing.T) {
	client, err := NewClient(Config{
		Provider: "gmail",
		Email:    "user@example.com",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("NewClient() client = nil, want client")
	}
}

func TestClientDeleteRequiresCriteria(t *testing.T) {
	client, err := NewClient(Config{
		Address:  "imap.example.com:993",
		Email:    "user@example.com",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Delete(context.Background(), DeleteCriteria{})
	if err == nil || !errors.Is(err, ErrDeleteCriteriaRequired) {
		t.Fatalf("Delete() error = %v, want criteria validation failure", err)
	}
}

func TestClientDeleteRequiresClient(t *testing.T) {
	var client *Client

	_, err := client.Delete(context.Background(), DeleteCriteria{})
	if err == nil || !errors.Is(err, ErrClientRequired) {
		t.Fatalf("Delete() error = %v, want client validation failure", err)
	}
}

func TestNewClientAcceptsRetryConfiguration(t *testing.T) {
	client, err := NewClient(Config{
		Address:               "imap.example.com:993",
		Email:                 "user@example.com",
		Password:              "secret",
		DeletePasses:          1,
		DeleteMailboxAttempts: 2,
		DeleteCommandRetries:  3,
		DeleteStoreRetries:    4,
		DeleteExpungeRetries:  5,
		MailboxCommandRetries: 6,
		FetchRetryAttempts:    7,
		BatchSize:             8,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("NewClient() client = nil, want client")
	}
}

func TestNewClientRejectsNegativeConfigValue(t *testing.T) {
	_, err := NewClient(Config{
		Address:   "imap.example.com:993",
		Email:     "user@example.com",
		Password:  "secret",
		BatchSize: -1,
	})
	if err == nil || !errors.Is(err, ErrNegativeConfigValue) {
		t.Fatalf("NewClient() error = %v, want ErrNegativeConfigValue", err)
	}
}
