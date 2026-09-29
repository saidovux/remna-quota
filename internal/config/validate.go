package config

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (c Config) validate() error {
	var errs []error
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error"))
	}
	if _, port, err := net.SplitHostPort(c.HTTPListenAddr); err != nil || port == "" {
		errs = append(errs, fmt.Errorf("HTTP_LISTEN_ADDR must be a host:port address"))
	}
	if c.WriteMode != "dry-run" && c.WriteMode != "live" {
		errs = append(errs, fmt.Errorf("WRITE_MODE must be dry-run or live"))
	}
	if c.Remnawave.AccessMode != "public_https" && c.Remnawave.AccessMode != "docker_bridge" {
		errs = append(errs, fmt.Errorf("REMNAWAVE_ACCESS_MODE must be public_https or docker_bridge"))
	}
	if c.Remnawave.AccessMode == "public_https" && c.Remnawave.BaseURL != nil && c.Remnawave.BaseURL.Scheme != "https" {
		errs = append(errs, fmt.Errorf("REMNAWAVE_BASE_URL must use https in public_https mode"))
	}
	if c.Remnawave.AccessMode == "docker_bridge" {
		if strings.TrimSpace(c.Remnawave.ForwardedFor) == "" || net.ParseIP(c.Remnawave.ForwardedFor) == nil {
			errs = append(errs, fmt.Errorf("REMNAWAVE_FORWARDED_FOR must be an IP address in docker_bridge mode"))
		}
		if c.Remnawave.ForwardedProto != "https" && c.Remnawave.ForwardedProto != "http" {
			errs = append(errs, fmt.Errorf("REMNAWAVE_FORWARDED_PROTO must be http or https"))
		}
	}
	if c.Database.MinConns > c.Database.MaxConns {
		errs = append(errs, fmt.Errorf("QUOTA_DB_MIN_CONNS cannot exceed QUOTA_DB_MAX_CONNS"))
	}
	if c.Scheduler.FastWatchPercent >= c.Scheduler.CriticalWatchPercent {
		errs = append(errs, fmt.Errorf("FAST_WATCH_PERCENT must be less than CRITICAL_WATCH_PERCENT"))
	}
	if c.ConnectionDropEnabled {
		errs = append(errs, fmt.Errorf("CONNECTION_DROP_ENABLED=true is not supported until the live contract is verified"))
	}
	if c.UsageAccountingMode != "strict_timestamp" && c.UsageAccountingMode != "calendar_day_utc_conservative" {
		errs = append(errs, fmt.Errorf("USAGE_ACCOUNTING_MODE must be strict_timestamp or calendar_day_utc_conservative"))
	}
	if c.Bedolaga.SubscriptionURLHost != "" {
		if strings.ContainsAny(c.Bedolaga.SubscriptionURLHost, "/:@") || strings.Contains(c.Bedolaga.SubscriptionURLHost, " ") {
			errs = append(errs, fmt.Errorf("BEDOLAGA_SUBSCRIPTION_URL_HOST must be a hostname without scheme or path"))
		}
	}
	managedOwners := map[string]string{}
	for _, key := range c.PoolOrder {
		p, ok := c.Pools[key]
		if !ok {
			continue
		}
		if p.AccountingSquadUUID != "" && !uuidPattern.MatchString(p.AccountingSquadUUID) {
			errs = append(errs, fmt.Errorf("pool %q has invalid accounting squad UUID", key))
		}
		seen := map[string]struct{}{}
		for _, id := range p.ManagedSquadUUIDs {
			if !uuidPattern.MatchString(id) {
				errs = append(errs, fmt.Errorf("pool %q has invalid managed squad UUID", key))
				continue
			}
			if _, exists := seen[strings.ToLower(id)]; exists {
				errs = append(errs, fmt.Errorf("pool %q contains duplicate managed squad UUID", key))
			}
			seen[strings.ToLower(id)] = struct{}{}
			normalizedID := strings.ToLower(id)
			if prior, exists := managedOwners[normalizedID]; exists && prior != key {
				errs = append(errs, fmt.Errorf("managed squad is assigned to both pools %q and %q", prior, key))
			} else {
				managedOwners[normalizedID] = key
			}
		}
	}
	if c.WriteMode == "live" {
		if c.Remnawave.BaseURL == nil {
			errs = append(errs, fmt.Errorf("REMNAWAVE_BASE_URL is required in live mode"))
		}
		if c.Remnawave.APIToken == "" {
			errs = append(errs, fmt.Errorf("REMNAWAVE_API_TOKEN is required in live mode"))
		}
		if c.Bedolaga.BaseURL == nil {
			errs = append(errs, fmt.Errorf("BEDOLAGA_BASE_URL is required in live mode"))
		}
		if c.Bedolaga.APIKey == "" {
			errs = append(errs, fmt.Errorf("BEDOLAGA_API_KEY is required in live mode"))
		}
		if c.Bedolaga.SubscriptionURLHost == "" {
			errs = append(errs, fmt.Errorf("BEDOLAGA_SUBSCRIPTION_URL_HOST is required in live mode"))
		}
		if c.Database.Password == "" {
			errs = append(errs, fmt.Errorf("QUOTA_DB_PASSWORD is required in live mode"))
		}
		for _, key := range c.PoolOrder {
			p := c.Pools[key]
			if p.AccountingSquadUUID == "" {
				errs = append(errs, fmt.Errorf("pool %q accounting squad UUID is required in live mode", key))
			}
			if len(p.ManagedSquadUUIDs) == 0 {
				errs = append(errs, fmt.Errorf("pool %q managed squad UUIDs are required in live mode", key))
			}
		}
		for _, key := range c.TariffOrder {
			t := c.Tariffs[key]
			if t.BedolagaID == "" {
				errs = append(errs, fmt.Errorf("tariff %q Bedolaga ID is required in live mode", key))
			}
			if len(t.Limits) == 0 {
				errs = append(errs, fmt.Errorf("tariff %q needs at least one pool limit in live mode", key))
			}
		}
	}
	return errors.Join(errs...)
}
