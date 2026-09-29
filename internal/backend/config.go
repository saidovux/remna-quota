package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saidovux/remna-quota/internal/providers"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	RemnawaveHWIDExternalSquad string
	EnableManagedAccounts      bool
	ListenAddr                 string
	PublicURL                  string
	APIKey                     string
	Provider                   string
	DatabaseURL                string
	SyncInterval               time.Duration
	RemnawaveURL               string
	RemnawaveToken             string
	RemnawaveProfiles          map[string][]string
	RemnawaveTimeout           time.Duration
	RemnawaveAllowHTTP         bool
	RemnawaveCaddyKey          string
	RemnawaveForwardedFor      string
	RemnawaveForwardedProto    string
	BrandName                  string
	BrandDescription           string
	BrandHomeURL               string
	SupportURL                 string
}

func LoadConfig() (Config, error) {
	env := func(key, fallback string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		RemnawaveHWIDExternalSquad: env("REMNAWAVE_HWID_EXTERNAL_SQUAD_UUID", ""),
		ListenAddr:                 env("BACKEND_LISTEN_ADDR", "127.0.0.1:8090"),
		PublicURL:                  env("BACKEND_PUBLIC_URL", "http://localhost:8090"),
		APIKey:                     env("BACKEND_API_KEY", ""),
		Provider:                   env("BACKEND_PROVIDER", "demo"),
		DatabaseURL:                env("BACKEND_DATABASE_URL", ""),
		RemnawaveURL:               env("REMNAWAVE_BASE_URL", ""),
		RemnawaveToken:             env("REMNAWAVE_API_TOKEN", ""),
		RemnawaveCaddyKey:          env("REMNAWAVE_CADDY_API_KEY", ""),
		RemnawaveForwardedFor:      env("REMNAWAVE_FORWARDED_FOR", ""),
		RemnawaveForwardedProto:    env("REMNAWAVE_FORWARDED_PROTO", ""),
		BrandName:                  env("BACKEND_BRAND_NAME", "Subscription"),
		BrandDescription:           env("BACKEND_BRAND_DESCRIPTION", ""),
		BrandHomeURL:               env("BACKEND_BRAND_HOME_URL", ""),
		SupportURL:                 env("BACKEND_SUPPORT_URL", ""),
	}
	switch env("BACKEND_ENABLE_MANAGED_ACCOUNTS", "true") {
	case "true":
		c.EnableManagedAccounts = true
	case "false":
	default:
		return c, errors.New("BACKEND_ENABLE_MANAGED_ACCOUNTS must be true or false")
	}
	if len(c.APIKey) < 32 || strings.ContainsAny(c.APIKey, "\r\n\t ") {
		return c, errors.New("BACKEND_API_KEY must contain at least 32 characters without whitespace; generate with quota-api keygen")
	}
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return c, errors.New("BACKEND_LISTEN_ADDR must be host:port")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("BACKEND_PUBLIC_URL must be an absolute HTTP(S) origin without credentials, path, or query")
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	c.BrandName = strings.TrimSpace(c.BrandName)
	if c.BrandName == "" || len([]rune(c.BrandName)) > 80 || strings.ContainsAny(c.BrandName, "\r\n\t") {
		return c, errors.New("BACKEND_BRAND_NAME must contain 1..80 characters without control characters")
	}
	c.BrandDescription = strings.TrimSpace(c.BrandDescription)
	if len([]rune(c.BrandDescription)) > 200 || strings.ContainsAny(c.BrandDescription, "\r\n\t") {
		return c, errors.New("BACKEND_BRAND_DESCRIPTION must be a single line of at most 200 characters")
	}
	for _, item := range []struct{ key, value string }{
		{"BACKEND_BRAND_HOME_URL", c.BrandHomeURL},
		{"BACKEND_SUPPORT_URL", c.SupportURL},
	} {
		if item.value == "" {
			continue
		}
		brandURL, err := url.Parse(item.value)
		if err != nil || brandURL.Host == "" || (brandURL.Scheme != "https" && brandURL.Scheme != "http") || brandURL.User != nil {
			return c, fmt.Errorf("%s must be an absolute HTTP(S) URL", item.key)
		}
	}
	c.SyncInterval, err = time.ParseDuration(env("BACKEND_SYNC_INTERVAL", "10s"))
	if err != nil || c.SyncInterval < 5*time.Second || c.SyncInterval > 24*time.Hour {
		return c, errors.New("BACKEND_SYNC_INTERVAL must be between 5s and 24h")
	}
	c.RemnawaveTimeout, err = time.ParseDuration(env("REMNAWAVE_REQUEST_TIMEOUT", "10s"))
	if err != nil || c.RemnawaveTimeout < time.Second || c.RemnawaveTimeout > 30*time.Second {
		return c, errors.New("REMNAWAVE_REQUEST_TIMEOUT must be between 1s and 30s")
	}
	switch env("REMNAWAVE_ALLOW_HTTP", "false") {
	case "true":
		c.RemnawaveAllowHTTP = true
	case "false":
	default:
		return c, errors.New("REMNAWAVE_ALLOW_HTTP must be true or false")
	}
	switch c.Provider {
	case "demo":
		if c.DatabaseURL != "" {
			return c, errors.New("demo uses ephemeral memory; omit BACKEND_DATABASE_URL")
		}
	case "remnawave":
		if c.DatabaseURL == "" || c.RemnawaveURL == "" || c.RemnawaveToken == "" {
			return c, errors.New("remnawave mode requires BACKEND_DATABASE_URL, REMNAWAVE_BASE_URL and REMNAWAVE_API_TOKEN")
		}
		if err := json.Unmarshal([]byte(env("REMNAWAVE_PROFILES_JSON", "{}")), &c.RemnawaveProfiles); err != nil {
			return c, errors.New("REMNAWAVE_PROFILES_JSON must map profile names to squad UUID arrays")
		}
		if c.EnableManagedAccounts && len(c.RemnawaveProfiles) == 0 {
			return c, errors.New("managed accounts require REMNAWAVE_PROFILES_JSON")
		}
		if _, err := pgxpool.ParseConfig(c.DatabaseURL); err != nil {
			return c, errors.New("invalid BACKEND_DATABASE_URL")
		}
		if _, err := providers.NewRemnawave(c.providerConfig()); err != nil {
			return c, errors.New("invalid Remnawave URL, credentials, profile UUIDs or request settings")
		}
	default:
		return c, fmt.Errorf("unsupported BACKEND_PROVIDER (use demo or remnawave)")
	}
	return c, nil
}

func (c Config) providerConfig() providers.RemnaConfig {
	return providers.RemnaConfig{HWIDExternalSquadUUID: c.RemnawaveHWIDExternalSquad, BaseURL: c.RemnawaveURL, APIToken: c.RemnawaveToken, Profiles: c.RemnawaveProfiles, Timeout: c.RemnawaveTimeout, AllowHTTP: c.RemnawaveAllowHTTP, CaddyAPIKey: c.RemnawaveCaddyKey, ForwardedFor: c.RemnawaveForwardedFor, ForwardedProto: c.RemnawaveForwardedProto}
}
