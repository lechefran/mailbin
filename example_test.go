package mailbin_test

import (
	"errors"
	"fmt"
	"time"

	"github.com/lechefran/mailbin"
)

func ExampleNewClient() {
	client, err := mailbin.NewClient(mailbin.Config{
		Provider: "gmail",
		Email:    "user@example.com",
		Password: "app-password",
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(client != nil)

	// Output:
	// true
}

func ExampleResolveIMAPAddress() {
	address, err := mailbin.ResolveIMAPAddress("icloud", "")
	if err != nil {
		panic(err)
	}

	fmt.Println(address)

	// Output:
	// imap.mail.me.com:993
}

func ExampleDeleteCriteria() {
	criteria := mailbin.DeleteCriteria{
		ReceivedBefore: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		FromAccounts: []string{
			"blocked@example.com",
		},
	}

	fmt.Println(criteria.ReceivedBefore.Format(time.RFC3339))
	fmt.Println(criteria.FromAccounts[0])

	// Output:
	// 2026-01-01T00:00:00Z
	// blocked@example.com
}

func ExampleDeleteIncompleteError() {
	err := &mailbin.DeleteIncompleteError{
		RemainingMatchingCount:      2,
		RemainingMatchingCountKnown: true,
	}

	fmt.Println(errors.Is(err, mailbin.ErrDeleteIncomplete))
	fmt.Println(err.Error())

	// Output:
	// true
	// delete incomplete: 2 emails still match delete criteria
}
