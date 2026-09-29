// Package subscription handles portable URI-list subscription documents.
// Structured Clash and sing-box configurations require a separate converter.
package subscription

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode"
)

var ErrInvalid = errors.New("invalid subscription link")

// LabelLink preserves connection settings and adds the pool label to the name.
// VMess names live inside base64 JSON; ShadowsocksR names live inside its payload.
func LabelLink(raw, label string) (string, error) {
	if raw == "" || len(raw) > 64*1024 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\x00") || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return "", ErrInvalid
	}
	prefix := "[" + label + "] "
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || rest == "" {
		return "", ErrInvalid
	}
	switch scheme {
	case "vmess":
		payload, err := decodeBase64(rest)
		if err != nil {
			return "", ErrInvalid
		}
		var config map[string]json.RawMessage
		if json.Unmarshal(payload, &config) != nil || config == nil {
			return "", ErrInvalid
		}
		// Missing addressing fields indicate a different, unsupported VMess format.
		if len(config["add"]) == 0 || len(config["id"]) == 0 || len(config["port"]) == 0 {
			return "", ErrInvalid
		}
		name := ""
		if value, exists := config["ps"]; exists && json.Unmarshal(value, &name) != nil {
			return "", ErrInvalid
		}
		config["ps"], _ = json.Marshal(prefix + name)
		out, err := json.Marshal(config)
		if err != nil {
			return "", ErrInvalid
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(out), nil
	case "ssr":
		payload, err := decodeBase64(rest)
		if err != nil || bytes.ContainsAny(payload, "\r\n\x00") {
			return "", ErrInvalid
		}
		endpoint, query, _ := strings.Cut(string(payload), "/?")
		if strings.Count(endpoint, ":") < 5 {
			return "", ErrInvalid
		}
		params, err := url.ParseQuery(query)
		if err != nil {
			return "", ErrInvalid
		}
		name := ""
		if params.Get("remarks") != "" {
			decoded, err := decodeBase64(params.Get("remarks"))
			if err != nil {
				return "", ErrInvalid
			}
			name = string(decoded)
		}
		params.Set("remarks", base64.RawURLEncoding.EncodeToString([]byte(prefix+name)))
		return "ssr://" + base64.RawURLEncoding.EncodeToString([]byte(endpoint+"/?"+params.Encode())), nil
	case "vless", "trojan", "ss", "hysteria2", "hy2", "tuic":
		// Keep the original connection part byte-for-byte. Parsing and rebuilding
		// URLs can alter plugin parameters and base64 Shadowsocks credentials.
		connection, fragment, _ := strings.Cut(raw, "#")
		u, err := url.Parse(connection)
		if err != nil || u.Host == "" {
			return "", ErrInvalid
		}
		name, err := url.PathUnescape(fragment)
		if err != nil {
			return "", ErrInvalid
		}
		return connection + "#" + url.PathEscape(prefix+name), nil
	default:
		return "", ErrInvalid
	}
}

// Encode produces the common plain or base64 URI-list wire format.
func Encode(links []string, format string) ([]byte, error) {
	if format != "plain" && format != "base64" {
		return nil, ErrInvalid
	}
	var document strings.Builder
	seen := make(map[string]bool)
	for _, link := range links {
		// Validate without changing the caller's already labeled name.
		if _, err := LabelLink(link, ""); err != nil {
			return nil, err
		}
		if !seen[link] {
			document.WriteString(link)
			document.WriteByte('\n')
			seen[link] = true
		}
	}
	if format == "plain" {
		return []byte(document.String()), nil
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(document.String()))), nil
}

func decodeBase64(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if data, err := encoding.DecodeString(value); err == nil {
			return data, nil
		}
	}
	return nil, ErrInvalid
}
