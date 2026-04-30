// Package mailbin provides a single-account IMAP delete client.
//
// The library focuses on deleting messages for one account at a time.
// Multi-account orchestration, configuration-file loading, prompting, and
// process-level logging belong in the consumer application.
//
// Typical usage is:
//
//	client, err := mailbin.NewClient(mailbin.Config{
//		Provider: "gmail",
//		Email:    "user@example.com",
//		Password: "app-password",
//	})
//	if err != nil {
//		// handle configuration or validation error
//	}
//
//	result, err := client.Delete(ctx, mailbin.DeleteCriteria{
//		ReceivedBefore: cutoff,
//		FromAccounts:   []string{"blocked@example.com"},
//	})
//	if err != nil {
//		if errors.Is(err, mailbin.ErrDeleteIncomplete) {
//			// inspect result for partial progress details
//		}
//	}
package mailbin
