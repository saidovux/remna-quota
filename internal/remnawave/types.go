package remnawave

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type Metadata struct {
	Version string `json:"version"`
	Build   struct {
		Time   string `json:"time"`
		Number string `json:"number"`
	} `json:"build"`
}

type User struct {
	ID                   int64           `json:"id"`
	ShortUUID            string          `json:"shortUuid"`
	Username             string          `json:"username"`
	Status               string          `json:"status"`
	TrafficLimitBytes    int64           `json:"trafficLimitBytes"`
	TrafficLimitStrategy string          `json:"trafficLimitStrategy"`
	ExpireAt             time.Time       `json:"expireAt"`
	ActiveInternalSquads []InternalSquad `json:"activeInternalSquads"`
}

func (u User) InSquad(uuid string) bool {
	for _, squad := range u.ActiveInternalSquads {
		if squad.UUID == uuid {
			return true
		}
	}
	return false
}

type InternalSquad struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	Info struct {
		MembersCount  int64 `json:"membersCount"`
		InboundsCount int64 `json:"inboundsCount"`
	} `json:"info"`
}

type Node struct {
	UUID              string   `json:"uuid"`
	Name              string   `json:"nodeName"`
	CountryCode       string   `json:"countryCode"`
	ConfigProfileUUID string   `json:"configProfileUuid"`
	ConfigProfileName string   `json:"configProfileName"`
	ActiveInbounds    []string `json:"activeInbounds"`
}

type DailyUsage struct {
	Days []UsageDay `json:"days"`
}

type UsageDay struct {
	Date  string      `json:"date"`
	Nodes []NodeUsage `json:"nodes"`
}

type NodeUsage struct {
	UUID       string `json:"uuid"`
	TotalBytes int64  `json:"totalBytes"`
}

func (u DailyUsage) TotalBytes() (int64, error) {
	var total int64
	for _, day := range u.Days {
		for _, node := range day.Nodes {
			if node.TotalBytes < 0 {
				return 0, fmt.Errorf("negative totalBytes for day %s", day.Date)
			}
			if node.TotalBytes > math.MaxInt64-total {
				return 0, errors.New("usage byte total overflow")
			}
			total += node.TotalBytes
		}
	}
	return total, nil
}

type APIError struct {
	StatusCode    int
	ErrorCode     string
	Message       string
	CorrelationID string
}

func (e *APIError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("Remnawave API status %d code %s: %s (correlation %s)", e.StatusCode, e.ErrorCode, e.Message, e.CorrelationID)
	}
	return fmt.Sprintf("Remnawave API status %d: %s (correlation %s)", e.StatusCode, e.Message, e.CorrelationID)
}
