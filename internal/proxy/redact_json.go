package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Redact JSON strings (including object keys) after unescaping them. Keep
// other tokens verbatim, including large integers and provider formatting.
func redactJSON(body []byte, secrets []string) []byte {
	return redactJSONLayer(body, secrets, true)
}

func redactJSONLayer(body []byte, secrets []string, nested bool) []byte {
	if len(secrets) == 0 || len(body) == 0 {
		return body
	}
	if !json.Valid(body) {
		return redact(body, secrets)
	}
	var out bytes.Buffer
	last := 0
	for i := 0; i < len(body); i++ {
		if body[i] != '"' {
			continue
		}
		start := i
		i++
		escaped := false
		for ; i < len(body) && body[i] != '"'; i++ {
			if body[i] == '\\' {
				escaped = true
				i++
			}
		}
		token := body[start : i+1]
		var value string
		if escaped {
			if err := json.Unmarshal(token, &value); err != nil {
				return []byte(`{"error":{"message":"invalid provider response"}}`)
			}
		} else {
			value = string(token[1 : len(token)-1])
		}
		safe := value
		if nested && strings.ContainsRune(value, '\\') {
			inner := strings.TrimSpace(value)
			if strings.HasPrefix(inner, "{") && json.Valid([]byte(inner)) {
				filtered := string(redactJSONLayer([]byte(inner), secrets, false))
				if filtered != inner {
					safe = strings.Replace(value, inner, filtered, 1)
				}
			}
		}
		for _, secret := range secrets {
			if secret != "" {
				mask := "[redacted]"
				if len(secret) < len(mask) {
					mask = ""
				}
				safe = strings.ReplaceAll(safe, secret, mask)
			}
		}
		if safe != value {
			encoded, _ := json.Marshal(safe)
			out.Write(body[last:start])
			out.Write(encoded)
			last = i + 1
		}
	}
	if last == 0 {
		return body
	}
	out.Write(body[last:])
	return out.Bytes()
}

// Decode data lines too: final snapshots and metadata are not always deltas.
func redactEventData(raw []byte, secrets []string) []byte {
	if len(secrets) == 0 {
		return raw
	}
	var out bytes.Buffer
	last := 0
	for start := 0; start < len(raw); {
		n := bytes.IndexByte(raw[start:], '\n')
		end := len(raw)
		if n >= 0 {
			end = start + n
		}
		line := raw[start:end]
		trim := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trim, []byte("data:")) {
			offset := end - len(trim) + 5
			data := raw[offset:end]
			safe := redactJSON(data, secrets)
			if !bytes.Equal(safe, data) {
				out.Write(raw[last:offset])
				out.Write(safe)
				last = end
			}
		}
		start = end + 1
	}
	if last == 0 {
		return redact(raw, secrets)
	}
	out.Write(raw[last:])
	return redact(out.Bytes(), secrets)
}
