// Package jsonbody edits a top-level JSON object while leaving nested values
// in their original encoding. Prefix caches depend on those bytes staying put.
package jsonbody

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Object is a top-level JSON object with stable key order.
type Object struct {
	Keys   []string
	Values map[string]json.RawMessage
}

// Parse reads a JSON object.
func Parse(body []byte) (Object, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	token, err := dec.Token()
	if err != nil {
		return Object{}, fmt.Errorf("request body is not JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return Object{}, fmt.Errorf("request body must be a JSON object")
	}
	obj := Object{Values: map[string]json.RawMessage{}}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return Object{}, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return Object{}, fmt.Errorf("request body must be a JSON object")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return Object{}, err
		}
		if _, exists := obj.Values[key]; !exists {
			obj.Keys = append(obj.Keys, key)
		}
		obj.Values[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return Object{}, err
	}
	return obj, nil
}

// Marshal writes the object in its current key order.
func (o Object) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, key := range o.Keys {
		raw, ok := o.Values[key]
		if !ok {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		encoded, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encoded)
		buf.WriteByte(':')
		buf.Write(raw)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Set replaces or appends a top-level value.
func (o *Object) Set(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, ok := o.Values[key]; !ok {
		o.Keys = append(o.Keys, key)
	}
	o.Values[key] = raw
	return nil
}

// Delete removes a top-level key.
func (o *Object) Delete(key string) {
	if _, ok := o.Values[key]; !ok {
		return
	}
	delete(o.Values, key)
	next := o.Keys[:0]
	for _, existing := range o.Keys {
		if existing != key {
			next = append(next, existing)
		}
	}
	o.Keys = next
}

// DropPrefix removes top-level keys with the given prefix.
func (o *Object) DropPrefix(prefix string) {
	kept := o.Keys[:0]
	for _, key := range o.Keys {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(o.Values, key)
			continue
		}
		kept = append(kept, key)
	}
	o.Keys = kept
}
