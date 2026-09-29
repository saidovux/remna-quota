package config

import (
	"strings"
	"testing"
)

func TestLoadDynamicPoolsAndTariffs(t *testing.T) {
	env := validTestEnv()
	cfg, err := load(mapLookup(env), environ(env))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Tariffs["standard"].Limits["main"]; got != 200*GiB {
		t.Fatalf("main limit = %d", got)
	}
	if got := cfg.Pools["main"].ManagedSquadUUIDs; len(got) != 1 || got[0] != testUUID1 {
		t.Fatalf("managed squads = %#v", got)
	}
	if cfg.SafeSummary().RemnawaveAPIToken != "<set>" {
		t.Fatal("safe summary did not redact API token")
	}
}

func TestRejectDuplicatePoolKey(t *testing.T) {
	env := validTestEnv()
	env["POOL_KEYS"] = "main, main"
	_, err := load(mapLookup(env), environ(env))
	assertErrorContains(t, err, "duplicate key")
}

func TestRejectNormalizationCollision(t *testing.T) {
	env := validTestEnv()
	env["POOL_KEYS"] = "cdn-ru,cdn_ru"
	delete(env, "POOL_MAIN_ACCOUNTING_SQUAD_UUID")
	delete(env, "POOL_MAIN_MANAGED_SQUAD_UUIDS")
	delete(env, "POOL_MAIN_WARNING_PERCENT")
	delete(env, "TARIFF_STANDARD_LIMIT_MAIN_GIB")
	env["POOL_CDN_RU_ACCOUNTING_SQUAD_UUID"] = testUUID1
	env["POOL_CDN_RU_MANAGED_SQUAD_UUIDS"] = testUUID1
	env["POOL_CDN_RU_WARNING_PERCENT"] = "80"
	env["TARIFF_STANDARD_LIMIT_CDN_RU_GIB"] = "50"
	_, err := load(mapLookup(env), environ(env))
	assertErrorContains(t, err, "normalize to CDN_RU")
}

func TestRejectInvalidUUID(t *testing.T) {
	env := validTestEnv()
	env["POOL_MAIN_ACCOUNTING_SQUAD_UUID"] = "not-a-uuid"
	_, err := load(mapLookup(env), environ(env))
	assertErrorContains(t, err, "invalid accounting squad UUID")
}

func TestRejectDuplicateTariffMapping(t *testing.T) {
	env := validTestEnv()
	env["TARIFF_KEYS"] = "standard,pro"
	env["TARIFF_PRO_BEDOLAGA_ID"] = env["TARIFF_STANDARD_BEDOLAGA_ID"]
	env["TARIFF_PRO_QUOTA_CYCLE_DAYS"] = "30"
	env["TARIFF_PRO_LIMIT_MAIN_GIB"] = "500"
	_, err := load(mapLookup(env), environ(env))
	assertErrorContains(t, err, "same Bedolaga ID")
}

func TestRejectUnknownTariffPoolLimit(t *testing.T) {
	env := validTestEnv()
	env["TARIFF_STANDARD_LIMIT_PREMIUM_GIB"] = "10"
	_, err := load(mapLookup(env), environ(env))
	assertErrorContains(t, err, "unknown tariff pool limit")
}

func TestLiveModeRequiresSecrets(t *testing.T) {
	env := validTestEnv()
	env["WRITE_MODE"] = "live"
	env["REMNAWAVE_API_TOKEN"] = ""
	env["BEDOLAGA_API_KEY"] = ""
	env["QUOTA_DB_PASSWORD"] = ""
	_, err := load(mapLookup(env), environ(env))
	assertErrorContains(t, err, "REMNAWAVE_API_TOKEN is required")
	assertErrorContains(t, err, "BEDOLAGA_API_KEY is required")
	assertErrorContains(t, err, "QUOTA_DB_PASSWORD is required")
}

func TestRejectEnabledConnectionDrop(t *testing.T) {
	env := validTestEnv()
	env["CONNECTION_DROP_ENABLED"] = "true"
	_, err := load(mapLookup(env), environ(env))
	assertErrorContains(t, err, "not supported")
}

const (
	testUUID1 = "11111111-1111-4111-8111-111111111111"
)

func validTestEnv() map[string]string {
	return map[string]string{
		"APP_ENV": "test", "WRITE_MODE": "dry-run", "LOG_LEVEL": "info", "HTTP_LISTEN_ADDR": "127.0.0.1:0",
		"REMNAWAVE_ACCESS_MODE": "public_https", "REMNAWAVE_BASE_URL": "https://panel.example.test", "REMNAWAVE_API_TOKEN": "secret",
		"BEDOLAGA_BASE_URL": "https://billing.example.test", "BEDOLAGA_API_KEY": "secret",
		"QUOTA_DB_HOST": "127.0.0.1", "QUOTA_DB_PORT": "5432", "QUOTA_DB_NAME": "quota", "QUOTA_DB_USER": "quota", "QUOTA_DB_PASSWORD": "secret",
		"POOL_KEYS": "main", "POOL_MAIN_ACCOUNTING_SQUAD_UUID": testUUID1, "POOL_MAIN_MANAGED_SQUAD_UUIDS": testUUID1, "POOL_MAIN_WARNING_PERCENT": "80",
		"TARIFF_KEYS": "standard", "TARIFF_STANDARD_BEDOLAGA_ID": "42", "TARIFF_STANDARD_QUOTA_CYCLE_DAYS": "30", "TARIFF_STANDARD_LIMIT_MAIN_GIB": "200",
	}
}

func mapLookup(env map[string]string) lookupFunc {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func environ(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}
