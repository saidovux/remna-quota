package remnawave

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSanitizedContractFixtures(t *testing.T) {
	userData, err := os.ReadFile("../../tests/fixtures/remnawave-user.json")
	if err != nil {
		t.Fatal(err)
	}
	var userEnvelope struct {
		Response User `json:"response"`
	}
	if err := json.Unmarshal(userData, &userEnvelope); err != nil {
		t.Fatal(err)
	}
	if userEnvelope.Response.ID <= 0 || userEnvelope.Response.ShortUUID == "" || userEnvelope.Response.Status == "" {
		t.Fatalf("invalid fixture: %+v", userEnvelope.Response)
	}

	usageData, err := os.ReadFile("../../tests/fixtures/remnawave-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	var usageEnvelope struct {
		Response DailyUsage `json:"response"`
	}
	if err := json.Unmarshal(usageData, &usageEnvelope); err != nil {
		t.Fatal(err)
	}
	total, err := usageEnvelope.Response.TotalBytes()
	if err != nil || total != 123456 {
		t.Fatalf("usage total=%d err=%v", total, err)
	}
}
