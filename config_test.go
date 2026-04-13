package mailbin

import (
	"strings"
	"testing"
)

func TestResolveIMAPAddress(t *testing.T) {
	testCases := []struct {
		name          string
		provider      string
		address       string
		wantAddress   string
		wantErrorText string
	}{
		{
			name:        "provider default",
			provider:    "gmail",
			wantAddress: string(GMAIL),
		},
		{
			name:        "provider alias",
			provider:    "office365",
			wantAddress: string(OUTLOOK),
		},
		{
			name:        "address override wins",
			provider:    "gmail",
			address:     "imap.custom.example:993",
			wantAddress: "imap.custom.example:993",
		},
		{
			name:          "missing provider and address",
			wantErrorText: "imap address or provider is required",
		},
		{
			name:          "unsupported provider",
			provider:      "fastmail",
			wantErrorText: `unsupported provider "fastmail"`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			address, err := ResolveIMAPAddress(testCase.provider, testCase.address)
			if testCase.wantErrorText != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErrorText) {
					t.Fatalf("ResolveIMAPAddress() error = %v, want %q", err, testCase.wantErrorText)
				}
				return
			}

			if err != nil {
				t.Fatalf("ResolveIMAPAddress() error = %v", err)
			}
			if address != testCase.wantAddress {
				t.Fatalf("ResolveIMAPAddress() = %q, want %q", address, testCase.wantAddress)
			}
		})
	}
}
