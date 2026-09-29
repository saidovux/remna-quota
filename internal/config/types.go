package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

const GiB int64 = 1024 * 1024 * 1024

type Config struct {
	AppEnv                string
	WriteMode             string
	LogLevel              string
	HTTPListenAddr        string
	Remnawave             Remnawave
	Bedolaga              Bedolaga
	Database              Database
	Scheduler             Scheduler
	Failure               FailurePolicy
	Pools                 map[string]TrafficPool
	Tariffs               map[string]Tariff
	PoolOrder             []string
	TariffOrder           []string
	ConnectionDropEnabled bool
	UsageAccountingMode   string
}

type Remnawave struct {
	AccessMode     string
	BaseURL        *url.URL
	APIToken       string
	CaddyAPIKey    string
	RequestTimeout time.Duration
	ForwardedFor   string
	ForwardedProto string
}

type Bedolaga struct {
	BaseURL             *url.URL
	APIKey              string
	RequestTimeout      time.Duration
	SubscriptionURLHost string
	UnassignedTariffID  string
}

type Database struct {
	Host           string
	Port           uint16
	Name           string
	User           string
	Password       string
	SSLMode        string
	MaxConns       int32
	MinConns       int32
	ConnectTimeout time.Duration
}

func (d Database) DSN() string {
	u := &url.URL{Scheme: "postgres", Host: fmt.Sprintf("%s:%d", d.Host, d.Port), Path: d.Name}
	u.User = url.UserPassword(d.User, d.Password)
	q := u.Query()
	q.Set("sslmode", d.SSLMode)
	q.Set("connect_timeout", fmt.Sprintf("%d", max(1, int(d.ConnectTimeout.Seconds()))))
	u.RawQuery = q.Encode()
	return u.String()
}

type Scheduler struct {
	SubscriptionSyncInterval   time.Duration
	NormalUsageInterval        time.Duration
	FastUsageInterval          time.Duration
	CriticalUsageInterval      time.Duration
	ExhaustedReconcileInterval time.Duration
	FastWatchPercent           int
	CriticalWatchPercent       int
	MaxConcurrentRemnawave     int
	MaxConcurrentBedolaga      int
}

type FailurePolicy struct {
	BedolagaOutageGracePeriod  time.Duration
	RequireNonOverlappingNodes bool
}

type TrafficPool struct {
	Key                 string
	AccountingSquadUUID string
	ManagedSquadUUIDs   []string
	WarningPercent      float64
}

type Tariff struct {
	Key            string
	BedolagaID     string
	QuotaCycleDays int
	Limits         map[string]int64
}

type SafeSummary struct {
	AppEnv                      string   `json:"app_env"`
	WriteMode                   string   `json:"write_mode"`
	LogLevel                    string   `json:"log_level"`
	HTTPListenAddr              string   `json:"http_listen_addr"`
	RemnawaveAccessMode         string   `json:"remnawave_access_mode"`
	RemnawaveBaseURL            string   `json:"remnawave_base_url"`
	RemnawaveAPIToken           string   `json:"remnawave_api_token"`
	RemnawaveCaddyKey           string   `json:"remnawave_caddy_api_key"`
	BedolagaBaseURL             string   `json:"bedolaga_base_url"`
	BedolagaAPIKey              string   `json:"bedolaga_api_key"`
	BedolagaSubscriptionURLHost string   `json:"bedolaga_subscription_url_host"`
	UsageAccountingMode         string   `json:"usage_accounting_mode"`
	DatabaseHost                string   `json:"quota_db_host"`
	DatabasePassword            string   `json:"quota_db_password"`
	Pools                       []string `json:"pools"`
	Tariffs                     []string `json:"tariffs"`
}

func (c Config) SafeSummary() SafeSummary {
	base := func(u *url.URL) string {
		if u == nil {
			return ""
		}
		return u.String()
	}
	set := func(v string) string {
		if strings.TrimSpace(v) == "" {
			return "<missing>"
		}
		return "<set>"
	}
	pools := append([]string(nil), c.PoolOrder...)
	tariffs := append([]string(nil), c.TariffOrder...)
	if len(pools) == 0 {
		for k := range c.Pools {
			pools = append(pools, k)
		}
		sort.Strings(pools)
	}
	if len(tariffs) == 0 {
		for k := range c.Tariffs {
			tariffs = append(tariffs, k)
		}
		sort.Strings(tariffs)
	}
	return SafeSummary{
		AppEnv: c.AppEnv, WriteMode: c.WriteMode, LogLevel: c.LogLevel,
		HTTPListenAddr:              c.HTTPListenAddr,
		RemnawaveAccessMode:         c.Remnawave.AccessMode,
		RemnawaveBaseURL:            base(c.Remnawave.BaseURL),
		RemnawaveAPIToken:           set(c.Remnawave.APIToken),
		RemnawaveCaddyKey:           set(c.Remnawave.CaddyAPIKey),
		BedolagaBaseURL:             base(c.Bedolaga.BaseURL),
		BedolagaAPIKey:              set(c.Bedolaga.APIKey),
		BedolagaSubscriptionURLHost: c.Bedolaga.SubscriptionURLHost,
		UsageAccountingMode:         c.UsageAccountingMode,
		DatabaseHost:                c.Database.Host,
		DatabasePassword:            set(c.Database.Password),
		Pools:                       pools, Tariffs: tariffs,
	}
}
