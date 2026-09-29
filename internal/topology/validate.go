package topology

import (
	"context"
	"fmt"

	"github.com/saidovux/remna-quota/internal/config"
	"github.com/saidovux/remna-quota/internal/remnawave"
)

type NodeReader interface {
	SquadAccessibleNodes(context.Context, string) ([]remnawave.Node, error)
}

func Validate(ctx context.Context, pools map[string]config.TrafficPool, order []string, requireNonOverlapping bool, reader NodeReader) error {
	owners := map[string]string{}
	for _, poolKey := range order {
		pool := pools[poolKey]
		nodes, err := reader.SquadAccessibleNodes(ctx, pool.AccountingSquadUUID)
		if err != nil {
			return fmt.Errorf("resolve nodes for pool %q: %w", poolKey, err)
		}
		if len(nodes) == 0 {
			return fmt.Errorf("pool %q accounting squad has no accessible nodes", poolKey)
		}
		for _, node := range nodes {
			if prior, exists := owners[node.UUID]; exists && prior != poolKey && requireNonOverlapping {
				return fmt.Errorf("pools %q and %q have overlapping physical node sets", prior, poolKey)
			}
			owners[node.UUID] = poolKey
		}
	}
	return nil
}
