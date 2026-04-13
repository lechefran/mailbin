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
	if err == nil || !errors.Is(err, ErrReceivedBeforeRequired) {
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
