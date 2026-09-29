package topology

import (
	"context"
	"strings"
	"testing"

	"github.com/saidovux/remna-quota/internal/config"
	"github.com/saidovux/remna-quota/internal/remnawave"
)

type fakeNodes map[string][]remnawave.Node

func (f fakeNodes) SquadAccessibleNodes(_ context.Context, squad string) ([]remnawave.Node, error) {
	return f[squad], nil
}

func TestTopologyDistinct(t *testing.T) {
	pools := testPools()
	reader := fakeNodes{"main-squad": {{UUID: "node-a"}}, "cdn-squad": {{UUID: "node-b"}}}
	if err := Validate(context.Background(), pools, []string{"main", "cdn"}, true, reader); err != nil {
		t.Fatal(err)
	}
}

func TestTopologyOverlap(t *testing.T) {
	pools := testPools()
	reader := fakeNodes{"main-squad": {{UUID: "node-a"}}, "cdn-squad": {{UUID: "node-a"}}}
	err := Validate(context.Background(), pools, []string{"main", "cdn"}, true, reader)
	if err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("err=%v", err)
	}
}

func testPools() map[string]config.TrafficPool {
	return map[string]config.TrafficPool{"main": {Key: "main", AccountingSquadUUID: "main-squad"}, "cdn": {Key: "cdn", AccountingSquadUUID: "cdn-squad"}}
}
