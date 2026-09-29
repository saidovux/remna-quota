package backend

import "testing"

func TestConfigModesAndSecretValidation(t *testing.T) {
	t.Setenv("BACKEND_ENABLE_MANAGED_ACCOUNTS", "false")
	for _, key := range []string{"BACKEND_LISTEN_ADDR", "BACKEND_PUBLIC_URL", "BACKEND_DATABASE_URL", "BACKEND_SYNC_INTERVAL", "REMNAWAVE_ALLOW_HTTP", "REMNAWAVE_REQUEST_TIMEOUT"} {
		t.Setenv(key, "")
	}
	t.Setenv("BACKEND_PROVIDER", "demo")
	t.Setenv("BACKEND_API_KEY", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("missing API key accepted")
	}
	t.Setenv("BACKEND_API_KEY", "local-test-key-with-at-least-32-characters")
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BACKEND_DATABASE_URL", "postgres://example")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("persistent demo accepted")
	}
	t.Setenv("BACKEND_DATABASE_URL", "")
	t.Setenv("BACKEND_PUBLIC_URL", "https://user:secret@example.com")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("credentials in public URL accepted")
	}
	t.Setenv("BACKEND_PUBLIC_URL", "https://subscriptions.example.com")
	t.Setenv("BACKEND_PROVIDER", "remnawave")
	t.Setenv("REMNAWAVE_BASE_URL", "")
	t.Setenv("REMNAWAVE_API_TOKEN", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("unconfigured provider accepted")
	}
}

func TestReadOnlyAggregationDoesNotRequireSquadsOrWriteMode(t *testing.T) {
	t.Setenv("BACKEND_PROVIDER", "remnawave")
	t.Setenv("BACKEND_API_KEY", "local-test-key-with-at-least-32-characters")
	t.Setenv("BACKEND_DATABASE_URL", "postgres://unused@localhost/unused")
	t.Setenv("BACKEND_ENABLE_MANAGED_ACCOUNTS", "false")
	t.Setenv("REMNAWAVE_BASE_URL", "https://panel.example")
	t.Setenv("REMNAWAVE_API_TOKEN", "read-only-key")
	t.Setenv("REMNAWAVE_PROFILES_JSON", "")
	cfg, err := LoadConfig()
	if err != nil || cfg.EnableManagedAccounts || len(cfg.RemnawaveProfiles) != 0 {
		t.Fatal("explicit read-only aggregation requires provisioning configuration")
	}
	t.Setenv("BACKEND_ENABLE_MANAGED_ACCOUNTS", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("managed mode accepted missing profiles")
	}
}

func TestManagedAccountsEnabledByDefault(t *testing.T) {
	t.Setenv("BACKEND_ENABLE_MANAGED_ACCOUNTS", "")
	t.Setenv("BACKEND_PROVIDER", "demo")
	t.Setenv("BACKEND_DATABASE_URL", "")
	t.Setenv("BACKEND_API_KEY", "local-test-key-with-at-least-32-characters")
	cfg, err := LoadConfig()
	if err != nil || !cfg.EnableManagedAccounts {
		t.Fatalf("managed default missing: %v", err)
	}
}
