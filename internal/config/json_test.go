package config

import "encoding/json"

func jsonUnmarshal(raw []byte, dest any) error { return json.Unmarshal(raw, dest) }
func jsonMarshal(v any) ([]byte, error)        { return json.Marshal(v) }
