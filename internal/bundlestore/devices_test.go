package bundlestore

import (
	"context"
	"errors"
	"fmt"
	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/internal/providers"
	"os"
	"sync"
	"testing"
	"time"
)

func TestPostgresSharedDevicesAcrossReplicasAndRestart(t *testing.T) {
	dsn := os.Getenv("BACKEND_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BACKEND_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	p, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p2, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	id := fmt.Sprintf("hwid_%d", time.Now().UnixNano())
	defer p.pool.Exec(context.Background(), "DELETE FROM account_bundles WHERE id=$1", id)
	adapters := map[string]bundle.Provider{"demo": providers.NewDemo()}
	a := bundle.NewService(p, adapters)
	b := bundle.NewService(p2, adapters)
	limit := 1
	account, err := a.Put(ctx, id, bundle.PutRequest{Username: id, DeviceLimit: &limit, Enabled: true, Parts: []bundle.PartRequest{{Key: "main", Provider: "demo", Profile: "main", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}, {Key: "cdn", Provider: "demo", Profile: "cdn", Enabled: true, ExpiresAt: time.Now().Add(time.Hour), LimitBytes: 50}}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := a
			if i%2 == 1 {
				svc = b
			}
			_, err := svc.LinksForDevice(ctx, account, bundle.Device{HWID: fmt.Sprintf("device-%010d", i)})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else if !errors.Is(err, bundle.ErrDeviceLimit) {
			t.Fatal(err)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted %d", admitted)
	}
	p3, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p3.Close()
	persisted, err := p3.Get(ctx, id)
	if err != nil || len(persisted.Devices) != 1 || bundle.DeviceLimit(persisted) != 1 {
		t.Fatal("device policy not durable", err)
	}
	restarted := bundle.NewService(p3, adapters)
	if _, err = restarted.LinksForDevice(ctx, persisted, persisted.Devices[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.LinksForDevice(ctx, persisted, bundle.Device{HWID: "device-9999999999"}); !errors.Is(err, bundle.ErrDeviceLimit) {
		t.Fatal("restart reset policy", err)
	}
}
