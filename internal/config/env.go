package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	return load(os.LookupEnv, os.Environ())
}

type lookupFunc func(string) (string, bool)

func load(get lookupFunc, environ []string) (Config, error) {
	var errs []error
	required := func(name string) string {
		v, ok := get(name)
		v = strings.TrimSpace(v)
		if !ok || v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
		return v
	}
	optional := func(name, fallback string) string {
		if v, ok := get(name); ok {
			return strings.TrimSpace(v)
		}
		return fallback
	}
	parseURL := func(name, raw string) *url.URL {
		if raw == "" {
			return nil
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Errorf("%s must be an absolute http(s) URL", name))
			return nil
		}
		u.Path = strings.TrimRight(u.Path, "/")
		return u
	}
	parseDuration := func(name, fallback string) time.Duration {
		raw := optional(name, fallback)
		v, err := time.ParseDuration(raw)
		if err != nil || v <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive duration", name))
			return 0
		}
		return v
	}
	parseInt := func(name, fallback string, minValue, maxValue int64) int64 {
		raw := optional(name, fallback)
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < minValue || v > maxValue {
			errs = append(errs, fmt.Errorf("%s must be in range %d..%d", name, minValue, maxValue))
			return 0
		}
		return v
	}
	parseBool := func(name, fallback string) bool {
		raw := optional(name, fallback)
		v, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s must be true or false", name))
			return false
		}
		return v
	}

	cfg := Config{
		AppEnv:         optional("APP_ENV", "development"),
		WriteMode:      optional("WRITE_MODE", "dry-run"),
		LogLevel:       optional("LOG_LEVEL", "info"),
		HTTPListenAddr: optional("HTTP_LISTEN_ADDR", "0.0.0.0:8090"),
		Remnawave: Remnawave{
			AccessMode: optional("REMNAWAVE_ACCESS_MODE", "public_https"),
			BaseURL:    parseURL("REMNAWAVE_BASE_URL", optional("REMNAWAVE_BASE_URL", "")),
			APIToken:   optional("REMNAWAVE_API_TOKEN", ""), CaddyAPIKey: optional("REMNAWAVE_CADDY_API_KEY", ""),
			RequestTimeout: parseDuration("REMNAWAVE_REQUEST_TIMEOUT", "10s"),
			ForwardedFor:   optional("REMNAWAVE_FORWARDED_FOR", "127.0.0.1"), ForwardedProto: optional("REMNAWAVE_FORWARDED_PROTO", "https"),
		},
		Bedolaga: Bedolaga{
			BaseURL: parseURL("BEDOLAGA_BASE_URL", optional("BEDOLAGA_BASE_URL", "")), APIKey: optional("BEDOLAGA_API_KEY", ""),
			RequestTimeout:      parseDuration("BEDOLAGA_REQUEST_TIMEOUT", "10s"),
			SubscriptionURLHost: strings.ToLower(strings.TrimSuffix(optional("BEDOLAGA_SUBSCRIPTION_URL_HOST", ""), ".")),
			UnassignedTariffID:  optional("BEDOLAGA_UNASSIGNED_TARIFF_ID", ""),
		},
		Database: Database{
			Host: required("QUOTA_DB_HOST"), Port: uint16(parseInt("QUOTA_DB_PORT", "5432", 1, 65535)),
			Name: required("QUOTA_DB_NAME"), User: required("QUOTA_DB_USER"), Password: optional("QUOTA_DB_PASSWORD", ""),
			SSLMode: optional("QUOTA_DB_SSLMODE", "disable"), MaxConns: int32(parseInt("QUOTA_DB_MAX_CONNS", "10", 1, math.MaxInt32)),
			MinConns: int32(parseInt("QUOTA_DB_MIN_CONNS", "1", 0, math.MaxInt32)), ConnectTimeout: parseDuration("QUOTA_DB_CONNECT_TIMEOUT", "5s"),
		},
		Scheduler: Scheduler{
			SubscriptionSyncInterval: parseDuration("SUBSCRIPTION_SYNC_INTERVAL", "45s"), NormalUsageInterval: parseDuration("NORMAL_USAGE_INTERVAL", "5m"),
			FastUsageInterval: parseDuration("FAST_USAGE_INTERVAL", "60s"), CriticalUsageInterval: parseDuration("CRITICAL_USAGE_INTERVAL", "25s"),
			ExhaustedReconcileInterval: parseDuration("EXHAUSTED_RECONCILE_INTERVAL", "60s"), FastWatchPercent: int(parseInt("FAST_WATCH_PERCENT", "80", 1, 99)),
			CriticalWatchPercent: int(parseInt("CRITICAL_WATCH_PERCENT", "95", 1, 99)), MaxConcurrentRemnawave: int(parseInt("MAX_CONCURRENT_REMNAWAVE_REQUESTS", "10", 1, 1000)),
			MaxConcurrentBedolaga: int(parseInt("MAX_CONCURRENT_BEDOLAGA_REQUESTS", "4", 1, 1000)),
		},
		Failure:               FailurePolicy{BedolagaOutageGracePeriod: parseDuration("BEDOLAGA_OUTAGE_GRACE_PERIOD", "15m"), RequireNonOverlappingNodes: parseBool("REQUIRE_NON_OVERLAPPING_NODE_SETS", "true")},
		ConnectionDropEnabled: parseBool("CONNECTION_DROP_ENABLED", "false"), Pools: map[string]TrafficPool{}, Tariffs: map[string]Tariff{},
		UsageAccountingMode: optional("USAGE_ACCOUNTING_MODE", "strict_timestamp"),
	}

	poolKeys, poolNorms := parseKeys("POOL_KEYS", required("POOL_KEYS"), &errs)
	cfg.PoolOrder = poolKeys
	for _, key := range poolKeys {
		norm := poolNorms[key]
		managed := splitCSV(optional("POOL_"+norm+"_MANAGED_SQUAD_UUIDS", ""))
		cfg.Pools[key] = TrafficPool{Key: key, AccountingSquadUUID: optional("POOL_"+norm+"_ACCOUNTING_SQUAD_UUID", ""), ManagedSquadUUIDs: managed, WarningPercent: float64(parseInt("POOL_"+norm+"_WARNING_PERCENT", "80", 1, 99))}
	}

	tariffKeys, tariffNorms := parseKeys("TARIFF_KEYS", required("TARIFF_KEYS"), &errs)
	cfg.TariffOrder = tariffKeys
	seenIDs := map[string]string{}
	for _, key := range tariffKeys {
		norm := tariffNorms[key]
		id := optional("TARIFF_"+norm+"_BEDOLAGA_ID", "")
		if id != "" {
			if prior, ok := seenIDs[id]; ok {
				errs = append(errs, fmt.Errorf("tariffs %q and %q map to the same Bedolaga ID", prior, key))
			} else {
				seenIDs[id] = key
			}
		}
		t := Tariff{Key: key, BedolagaID: id, QuotaCycleDays: int(parseInt("TARIFF_"+norm+"_QUOTA_CYCLE_DAYS", "30", 1, 3650)), Limits: map[string]int64{}}
		for _, poolKey := range poolKeys {
			name := "TARIFF_" + norm + "_LIMIT_" + poolNorms[poolKey] + "_GIB"
			if raw, ok := get(name); ok && strings.TrimSpace(raw) != "" {
				gib, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err != nil || gib <= 0 || gib > math.MaxInt64/GiB {
					errs = append(errs, fmt.Errorf("%s must be a positive, representable GiB value", name))
				} else {
					t.Limits[poolKey] = gib * GiB
				}
			}
		}
		cfg.Tariffs[key] = t
	}
	rejectUnknownTariffLimits(environ, tariffNorms, poolNorms, &errs)

	if err := cfg.validate(); err != nil {
		errs = append(errs, err)
	}
	return cfg, errors.Join(errs...)
}

func parseKeys(name, raw string, errs *[]error) ([]string, map[string]string) {
	items := splitCSV(raw)
	keys := make([]string, 0, len(items))
	byKey := map[string]struct{}{}
	byNorm := map[string]string{}
	norms := map[string]string{}
	for _, key := range items {
		if !keyPattern.MatchString(key) {
			*errs = append(*errs, fmt.Errorf("%s contains invalid key %q", name, key))
			continue
		}
		if _, exists := byKey[key]; exists {
			*errs = append(*errs, fmt.Errorf("%s contains duplicate key %q", name, key))
			continue
		}
		byKey[key] = struct{}{}
		norm := normalizeKey(key)
		if prior, exists := byNorm[norm]; exists {
			*errs = append(*errs, fmt.Errorf("%s keys %q and %q normalize to %s", name, prior, key, norm))
			continue
		}
		byNorm[norm] = key
		norms[key] = norm
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		*errs = append(*errs, fmt.Errorf("%s must contain at least one key", name))
	}
	return keys, norms
}

func splitCSV(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func normalizeKey(key string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", "_", "_").Replace(key))
}

func rejectUnknownTariffLimits(environ []string, tariffNorms, poolNorms map[string]string, errs *[]error) {
	known := map[string]struct{}{}
	for _, tn := range tariffNorms {
		for _, pn := range poolNorms {
			known["TARIFF_"+tn+"_LIMIT_"+pn+"_GIB"] = struct{}{}
		}
	}
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, "TARIFF_") || !strings.Contains(name, "_LIMIT_") || !strings.HasSuffix(name, "_GIB") {
			continue
		}
		if _, ok := known[name]; !ok {
			*errs = append(*errs, fmt.Errorf("unknown tariff pool limit variable %s", name))
		}
	}
}
