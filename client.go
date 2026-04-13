package mailbin

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type DeleteCriteria struct {
	ReceivedBefore time.Time
}

type DeleteResult struct {
	Deleted []MessageSummary
}

func NewClient(config Config) (*Client, error) {
	config.Provider = strings.TrimSpace(config.Provider)
	config.Email = strings.TrimSpace(config.Email)

	address, err := ResolveIMAPAddress(config.Provider, config.Address)
	if err != nil {
		return nil, err
	}
	config.Address = address

	client := &Client{config: config}
	if err := client.validate(); err != nil {
		return nil, err
	}

	return client, nil
}

func (c *Client) Delete(ctx context.Context, criteria DeleteCriteria) (DeleteResult, error) {
	if c == nil {
		return DeleteResult{}, fmt.Errorf("client is required")
	}
	if criteria.ReceivedBefore.IsZero() {
		return DeleteResult{}, fmt.Errorf("received before is required")
	}

	session, err := c.login(ctx)
	if err != nil {
		return DeleteResult{}, err
	}

	deleted, deleteErr := session.deleteBefore(criteria.ReceivedBefore)
	logoutErr := session.Logout()

	result := DeleteResult{Deleted: deleted}
	if deleteErr != nil {
		return result, deleteErr
	}
	if logoutErr != nil && !isTimeoutError(logoutErr) {
		return result, logoutErr
	}

	return result, nil
}
