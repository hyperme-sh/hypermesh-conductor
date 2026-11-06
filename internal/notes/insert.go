package notes

import (
	"bytes"
	"encoding/json"
)

// InsertAfterPrefix places the retrieved block after system messages and
// before the rest of the conversation. System message bytes are copied through.
func InsertAfterPrefix(body []byte, block string) ([]byte, error) {
	if stringsTrim(block) == "" {
		return body, nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	token, err := dec.Token()
	if err != nil {
		return body, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return body, nil
	}
	type field struct {
		key string
		raw json.RawMessage
	}
	var fields []field
	index := map[string]int{}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return body, err
		}
		key, _ := keyToken.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return body, err
		}
		index[key] = len(fields)
		fields = append(fields, field{key: key, raw: raw})
	}
	message, err := json.Marshal(map[string]string{"role": "user", "content": block})
	if err != nil {
		return body, err
	}
	var messages []json.RawMessage
	if pos, ok := index["messages"]; ok {
		messages, err = decodeRawArray(fields[pos].raw)
		if err != nil {
			return body, err
		}
	}
	var prefix, tail []json.RawMessage
	seenOther := false
	for _, item := range messages {
		if !seenOther && rawRole(item) == "system" {
			prefix = append(prefix, item)
			continue
		}
		seenOther = true
		tail = append(tail, item)
	}
	combined := append(append(prefix, message), tail...)
	encoded := joinRaw(combined)
	if pos, ok := index["messages"]; ok {
		fields[pos].raw = encoded
	} else {
		fields = append(fields, field{key: "messages", raw: encoded})
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, item := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(item.key)
		if err != nil {
			return body, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(item.raw)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func decodeRawArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '[' {
		return nil, nil
	}
	var items []json.RawMessage
	for dec.More() {
		var item json.RawMessage
		if err := dec.Decode(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func rawRole(raw json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	role, _ := obj["role"].(string)
	return role
}

func joinRaw(items []json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(item)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

func stringsTrim(text string) string {
	for len(text) > 0 && (text[0] == ' ' || text[0] == '\n' || text[0] == '\t') {
		text = text[1:]
	}
	return text
}
