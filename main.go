package mailbin

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const defaultAccountTimeout = 30 * time.Second

type App struct {
	Client         *IMAPClient
	Accounts       []ConfiguredAccount
	Login          func(context.Context, *IMAPClient) (DeleteSession, error)
	Age            int
	IncludeFlagged bool
	Concurrency    int
	Timeout        time.Duration
	Now            func() time.Time
}

type ConfiguredAccount struct {
	Name   string
	Client *IMAPClient
}

type DeleteSession interface {
	DeleteInboxOlderThanDays(time.Time, int, bool) ([]EmailSummary, error)
	Logout() error
}

type AccountFailure struct {
	AccountName string
	Err         error
}

type RunResult struct {
	Emails        []EmailSummary
	TotalAccounts int
	Failures      []AccountFailure
}

func (r RunResult) SuccessfulAccounts() int {
	successful := r.TotalAccounts - len(r.Failures)
	if successful < 0 {
		return 0
	}

	return successful
}

func (a *App) Run(ctx context.Context) (*RunResult, error) {
	if a == nil {
		return nil, fmt.Errorf("app is required")
	}

	accounts, err := a.resolveAccounts()
	if err != nil {
		return nil, err
	}

	timeout := a.Timeout
	if timeout <= 0 {
		timeout = defaultAccountTimeout
	}

	login := a.Login
	if login == nil {
		login = func(ctx context.Context, client *IMAPClient) (DeleteSession, error) {
			return client.Login(ctx)
		}
	}

	type accountRunResult struct {
		accountName string
		emails      []EmailSummary
		err         error
	}

	results := make(chan accountRunResult, len(accounts))
	var sem chan struct{}
	if a.Concurrency > 0 {
		sem = make(chan struct{}, a.Concurrency)
	}
	var wg sync.WaitGroup
	for _, account := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() {
					<-sem
				}()
			}

			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			log.Printf("starting account %s delete", account.Name)
			session, err := login(runCtx, account.Client)
			if err != nil {
				results <- accountRunResult{
					accountName: account.Name,
					err:         err,
				}
				return
			}

			log.Printf("connected to IMAP server %s as %s", account.Client.Address, account.Client.Email)

			emails, err := a.deleteByAge(session)
			logoutErr := session.Logout()
			if len(accounts) > 1 {
				for index := range emails {
					emails[index].Account = account.Name
				}
			}
			if err != nil {
				results <- accountRunResult{
					accountName: account.Name,
					emails:      emails,
					err:         err,
				}
				return
			}
			if logoutErr != nil {
				if isTimeoutError(logoutErr) {
					log.Printf("ignoring logout timeout for account %s: %v", account.Name, logoutErr)
					logoutErr = nil
				}
			}
			if logoutErr != nil {
				results <- accountRunResult{
					accountName: account.Name,
					emails:      emails,
					err:         logoutErr,
				}
				return
			}

			log.Printf("finished deletion for account %s: deleted %d emails", account.Name, len(emails))

			results <- accountRunResult{
				accountName: account.Name,
				emails:      emails,
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var allEmails []EmailSummary
	var failures []AccountFailure
	for result := range results {
		allEmails = append(allEmails, result.emails...)
		if result.err != nil {
			failures = append(failures, AccountFailure{
				AccountName: result.accountName,
				Err:         result.err,
			})
			continue
		}
	}

	runResult := &RunResult{
		Emails:        allEmails,
		TotalAccounts: len(accounts),
		Failures:      failures,
	}

	if len(failures) > 0 {
		return runResult, aggregateAccountFailures(failures)
	}

	return runResult, nil
}

func (a *App) resolveAccounts() ([]ConfiguredAccount, error) {
	if len(a.Accounts) > 0 {
		return a.Accounts, nil
	}
	if a.Client != nil {
		return []ConfiguredAccount{
			{
				Name:   defaultAccountName(a.Client.Email),
				Client: a.Client,
			},
		}, nil
	}

	return nil, fmt.Errorf("at least one account is required")
}

func (a *App) deleteByAge(session DeleteSession) ([]EmailSummary, error) {
	if a.Age < 0 {
		return nil, fmt.Errorf("age is required and must be 0 or greater")
	}

	now := time.Now
	if a.Now != nil {
		now = a.Now
	}

	return session.DeleteInboxOlderThanDays(now(), a.Age, a.IncludeFlagged)
}

func aggregateAccountFailures(failures []AccountFailure) error {
	if len(failures) == 0 {
		return nil
	}

	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		parts = append(parts, fmt.Sprintf("%s: %v", failure.AccountName, failure.Err))
	}

	return fmt.Errorf("%d account(s) failed: %s", len(failures), strings.Join(parts, "; "))
}

func defaultAccountName(email string) string {
	if email == "" {
		return "account"
	}

	return email
}
