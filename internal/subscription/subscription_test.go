package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestConnectionSettingsPreserved(t *testing.T) {
	connection := "vless://uuid@example.com:443?type=ws&path=%2Fvpn%3Fed%3D2048&security=tls&sni=example.com"
	got, err := LabelLink(connection+"#Frankfurt%20%2B%20fast", "CDN")
	if err != nil {
		t.Fatal(err)
	}
	want := connection + "#" + url.PathEscape("[CDN] Frankfurt + fast")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	for _, scheme := range []string{"trojan", "ss", "hysteria2", "hy2", "tuic"} {
		if _, err := LabelLink(scheme+"://secret@example.com:443#name", "MAIN"); err != nil {
			t.Errorf("%s: %v", scheme, err)
		}
	}
}

func TestVMessNameInsidePayload(t *testing.T) {
	payload := `{"v":"2","ps":"Paris","add":"example.com","port":"443","id":"a-user-id","net":"ws","path":"/vpn"}`
	got, err := LabelLink("vmess://"+base64.RawStdEncoding.EncodeToString([]byte(payload)), "MAIN")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "vmess://"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err = json.Unmarshal(decoded, &config); err != nil {
		t.Fatal(err)
	}
	if config["ps"] != "[MAIN] Paris" || config["path"] != "/vpn" || config["id"] != "a-user-id" {
		t.Fatalf("unexpected rewritten payload: %s", decoded)
	}
}

func TestSSRNameInsidePayload(t *testing.T) {
	connection := "example.com:443:origin:aes-256-cfb:plain:cGFzcw"
	payload := connection + "/?obfsparam=aG9zdA&remarks=" + base64.RawURLEncoding.EncodeToString([]byte("Tokyo"))
	got, err := LabelLink("ssr://"+base64.RawURLEncoding.EncodeToString([]byte(payload)), "CDN")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBase64(strings.TrimPrefix(got, "ssr://"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, query, _ := strings.Cut(string(decoded), "/?")
	params, _ := url.ParseQuery(query)
	name, _ := decodeBase64(params.Get("remarks"))
	if endpoint != connection || params.Get("obfsparam") != "aG9zdA" || string(name) != "[CDN] Tokyo" {
		t.Fatalf("unexpected payload %s", decoded)
	}
}

func TestRejectsNonURIAndMalformedProviders(t *testing.T) {
	for _, raw := range []string{"", "https://evil.example/config", "file:///etc/passwd", "vless://", "vless://host#bad%xy", "vless://host\nss://evil", "vmess://%%%", "vmess://e30=", "ssr://e30=", `{"outbounds":[]}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := LabelLink(raw, "MAIN"); err == nil {
				t.Fatalf("accepted invalid link %q", raw)
			}
		})
	}
}

func TestEncodeRoundtripAndDedup(t *testing.T) {
	links := []string{"vless://a@main.example:443#MAIN", "vless://b@cdn.example:443#CDN", "vless://a@main.example:443#MAIN"}
	plain, err := Encode(links, "plain")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(links, "base64")
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(string(encoded))
	if string(plain) != string(decoded) || strings.Count(string(plain), "\n") != 2 {
		t.Fatalf("invalid merge %q", plain)
	}
	if _, err := Encode(links, "clash"); err == nil {
		t.Fatal("accepted unsupported format")
	}
}
