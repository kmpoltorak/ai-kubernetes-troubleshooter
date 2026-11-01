package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

var sensitiveKey = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|credentials?|authorization|cookie)$`)

// JSON masks credential fields and string values without changing unrelated
// keys, numbers or types. It never returns malformed input in an error.
func JSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, errors.New("invalid JSON for redaction")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing JSON data for redaction")
	}
	return json.Marshal(clean(v))
}

func clean(v any) any {
	switch v := v.(type) {
	case string:
		return Text(v)
	case []any:
		for i := range v {
			v[i] = clean(v[i])
		}
	case map[string]any:
		for key, value := range v {
			if sensitiveKey.MatchString(key) {
				v[key] = "[REDACTED]"
			} else {
				v[key] = clean(value)
			}
		}
	}
	return v
}

// Field also masks short credentials identified only by their field name.
func Field(key, value string) string {
	if sensitiveKey.MatchString(key) {
		return "[REDACTED]"
	}
	return Text(value)
}
