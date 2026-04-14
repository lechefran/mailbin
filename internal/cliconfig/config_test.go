package cliconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
			password, err := ResolvePassword(
				strings.NewReader(testCase.input),
				prompt,
				func(string) string { return testCase.envValue },
				testCase.interactive,
			)

			if testCase.wantErrorText != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErrorText) {
					t.Fatalf("ResolvePassword() error = %v, want %q", err, testCase.wantErrorText)
				}
				return
			}

			if err != nil {
				t.Fatalf("ResolvePassword() error = %v", err)
			}
			if password != testCase.wantPassword {
				t.Fatalf("ResolvePassword() password = %q, want %q", password, testCase.wantPassword)
			}
			if prompt.String() != testCase.wantPrompt {
				t.Fatalf("ResolvePassword() prompt = %q, want %q", prompt.String(), testCase.wantPrompt)
			}
		})
	}
}

func TestLoadAccountsUsesProviderDefaults(t *testing.T) {
	configPath := writeAccountsConfig(t, `{
  "accounts": [
    {
      "name": "gmail",
      "email": "one@example.com",
      "provider": "gmail",
      "password_env": "MAILBIN_GMAIL_PASSWORD"
    },
    {
      "name": "icloud",
      "email": "two@example.com",
      "provider": "icloud",
      "password_env": "MAILBIN_ICLOUD_PASSWORD"
    }
  ]
}`)

	accounts, err := LoadAccounts(
		configPath,
		"",
		strings.NewReader(""),
		&bytes.Buffer{},
		func(key string) string {
			switch key {
			case "MAILBIN_GMAIL_PASSWORD":
				return "gmail-secret"
			case "MAILBIN_ICLOUD_PASSWORD":
				return "icloud-secret"
			default:
				return ""
			}
		},
		false,
	)
	if err != nil {
		t.Fatalf("LoadAccounts() error = %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("LoadAccounts() count = %d, want 2", len(accounts))
	}
	if accounts[0].Config.Address != mailbin.GMAIL {
		t.Fatalf("first account address = %q, want %q", accounts[0].Config.Address, mailbin.GMAIL)
	}
	if accounts[1].Config.Address != mailbin.ICLOUD {
		t.Fatalf("second account address = %q, want %q", accounts[1].Config.Address, mailbin.ICLOUD)
	}
	if accounts[0].Config.Password != "gmail-secret" || accounts[1].Config.Password != "icloud-secret" {
		t.Fatalf("account passwords = %#v, want provider env passwords", accounts)
	}
}

func TestLoadAccountsSelectsOneAccount(t *testing.T) {
	configPath := writeAccountsConfig(t, `{
  "accounts": [
    {
      "name": "gmail",
      "email": "one@example.com",
      "provider": "gmail",
      "password_env": "MAILBIN_GMAIL_PASSWORD"
    },
    {
      "name": "icloud",
      "email": "two@example.com",
      "provider": "icloud",
      "password_env": "MAILBIN_ICLOUD_PASSWORD"
    }
  ]
}`)

	accounts, err := LoadAccounts(
		configPath,
		"icloud",
		strings.NewReader(""),
		&bytes.Buffer{},
		func(key string) string {
			if key == "MAILBIN_ICLOUD_PASSWORD" {
				return "icloud-secret"
			}
			return ""
		},
		false,
	)
	if err != nil {
		t.Fatalf("LoadAccounts() error = %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("LoadAccounts() count = %d, want 1", len(accounts))
	}
	if accounts[0].Name != "icloud" {
		t.Fatalf("selected account = %q, want icloud", accounts[0].Name)
	}
	if accounts[0].Config.Address != mailbin.ICLOUD {
		t.Fatalf("selected account address = %q, want %q", accounts[0].Config.Address, mailbin.ICLOUD)
	}
}

func TestLoadAccountsUsesAddressOverride(t *testing.T) {
	configPath := writeAccountsConfig(t, `{
  "accounts": [
    {
      "name": "custom",
      "email": "custom@example.com",
      "provider": "gmail",
      "imap_addr": "imap.custom.example:993",
      "password_env": "MAILBIN_CUSTOM_PASSWORD"
    }
  ]
}`)

	accounts, err := LoadAccounts(
		configPath,
		"",
		strings.NewReader(""),
		&bytes.Buffer{},
		func(key string) string {
			if key == "MAILBIN_CUSTOM_PASSWORD" {
				return "custom-secret"
			}
			return ""
		},
		false,
	)
	if err != nil {
		t.Fatalf("LoadAccounts() error = %v", err)
	}
	if accounts[0].Config.Address != "imap.custom.example:993" {
		t.Fatalf("override address = %q, want custom address", accounts[0].Config.Address)
	}
}

func writeAccountsConfig(t *testing.T, contents string) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return configPath
}
