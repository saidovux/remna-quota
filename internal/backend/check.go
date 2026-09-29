package backend

import (
	"context"
	"errors"
	"github.com/saidovux/remna-quota/internal/providers"
)

func CheckProvider(ctx context.Context, cfg Config) (providers.CheckReport, error) {
	if cfg.Provider != "remnawave" {
		return providers.CheckReport{}, errors.New("check-provider requires BACKEND_PROVIDER=remnawave")
	}
	p, err := providers.NewRemnawave(cfg.providerConfig())
	if err != nil {
		return providers.CheckReport{}, errors.New("invalid Remnawave provider configuration")
	}
	if cfg.EnableManagedAccounts {
		return p.Check(ctx), nil
	}
	return p.CheckReferences(ctx), nil
}
