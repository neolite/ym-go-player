package ymapi

import "context"

// AccountStatus — то, что нужно знать на старте: кто мы, есть ли Плюс, какой регион.
type AccountStatus struct {
	UID     int64
	Login   string
	Region  int
	HasPlus bool
}

type accountStatusResult struct {
	Account struct {
		UID    int64  `json:"uid"`
		Login  string `json:"login"`
		Region int    `json:"region"`
	} `json:"account"`
	Plus struct {
		HasPlus bool `json:"hasPlus"`
	} `json:"plus"`
}

// AccountStatus проверяет валидность токена и наличие подписки.
// Вызывается на старте: без Плюса плеер не имеет смысла.
func (c *Client) AccountStatus(ctx context.Context) (*AccountStatus, error) {
	var res accountStatusResult
	if err := c.Get(ctx, "/account/status", nil, &res); err != nil {
		return nil, err
	}
	return &AccountStatus{
		UID:     res.Account.UID,
		Login:   res.Account.Login,
		Region:  res.Account.Region,
		HasPlus: res.Plus.HasPlus,
	}, nil
}
