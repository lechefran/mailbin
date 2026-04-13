package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lechefran/mailbin"
)

type accountsConfig struct {
	Accounts []accountConfig `json:"accounts"`
}

type accountConfig struct {
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Address     string `json:"imap_addr"`
	Email       string `json:"email"`
	PasswordEnv string `json:"password_env"`
}

func loadConfiguredAccounts(
	path string,
	selectedAccount string,
	input io.Reader,
	prompt io.Writer,
	getenv func(string) string,
	interactive bool,
) ([]configuredAccount, error) {
	config, err := readAccountsConfig(path)
	if err != nil {
		return nil, err
	}

	selectedAccount = strings.TrimSpace(selectedAccount)
	accounts := make([]configuredAccount, 0, len(config.Accounts))
	for _, account := range config.Accounts {
		accountName := defaultAccountName(account.Name, account.Email)
		if selectedAccount != "" && accountName != selectedAccount {
			continue
		}

		password, err := resolveConfiguredAccountPassword(account, input, prompt, getenv, interactive)
		if err != nil {
			return nil, err
		}

		address, err := mailbin.ResolveIMAPAddress(account.Provider, account.Address)
		if err != nil {
			return nil, fmt.Errorf("resolve IMAP address for %q: %w", accountName, err)
		}

		accounts = append(accounts, configuredAccount{
			Name: accountName,
			Config: mailbin.Config{
				Provider: strings.TrimSpace(account.Provider),
				Address:  address,
				Email:    strings.TrimSpace(account.Email),
				Password: password,
			},
		})
	}

	if len(accounts) == 0 {
		if selectedAccount != "" {
			return nil, fmt.Errorf("account %q was not found in %s", selectedAccount, path)
		}
		return nil, fmt.Errorf("no accounts found in %s", path)
	}

	return accounts, nil
}

func readAccountsConfig(path string) (*accountsConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open accounts config: %w", err)
	}
	defer file.Close()

	var config accountsConfig
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		return nil, fmt.Errorf("decode accounts config: %w", err)
	}

	return &config, nil
}

func resolveConfiguredAccountPassword(
	account accountConfig,
	input io.Reader,
	prompt io.Writer,
	getenv func(string) string,
	interactive bool,
) (string, error) {
	passwordEnv := strings.TrimSpace(account.PasswordEnv)
	if passwordEnv != "" {
		if password := getenv(passwordEnv); password != "" {
			return password, nil
		}
		if !interactive {
			return "", fmt.Errorf("%s is required for account %q", passwordEnv, defaultAccountName(account.Name, account.Email))
		}
	}

	if !interactive {
		return "", fmt.Errorf("password is required for account %q when stdin is not interactive", defaultAccountName(account.Name, account.Email))
	}

	promptText := fmt.Sprintf("Enter IMAP password for %s: ", defaultAccountName(account.Name, account.Email))
	return promptPassword(input, prompt, promptText)
}

func promptPassword(input io.Reader, prompt io.Writer, promptText string) (string, error) {
	if _, err := io.WriteString(prompt, promptText); err != nil {
		return "", err
	}

	password, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read password: %w", err)
	}

	password = strings.TrimRight(password, "\r\n")
	if password == "" {
		return "", fmt.Errorf("password is required")
	}

	return password, nil
}

func defaultAccountName(name, email string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}

	email = strings.TrimSpace(email)
	if email != "" {
		return email
	}

	return "account"
}
