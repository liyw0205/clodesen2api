package config

import "errors"

type CacheSettings struct {
	AccountsSeconds int `json:"accountsSeconds"`
	ModelsSeconds   int `json:"modelsSeconds"`
	CreditsSeconds  int `json:"creditsSeconds"`
}

func (c CacheSettings) defaults() CacheSettings {
	for _, value := range []*int{&c.AccountsSeconds, &c.ModelsSeconds, &c.CreditsSeconds} {
		if *value == 0 {
			*value = 3600
		}
	}
	return c
}

func (c CacheSettings) validate(previous CacheSettings) (CacheSettings, error) {
	// Preserve settings when an older management client omits the new block.
	if c == (CacheSettings{}) {
		c = previous
	}
	c = c.defaults()
	for _, value := range []int{c.AccountsSeconds, c.ModelsSeconds, c.CreditsSeconds} {
		if value < 30 || value > 86400 {
			return c, errors.New("缓存周期必须在 30 到 86400 秒之间")
		}
	}
	return c, nil
}
