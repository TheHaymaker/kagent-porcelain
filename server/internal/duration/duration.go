// Package duration lets configuration files write durations as "30s".
package duration

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration that reads "30s" or a number of seconds.
type Duration time.Duration

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		p, err := time.ParseDuration(x)
		if err != nil {
			return err
		}
		*d = Duration(p)
	case float64:
		*d = Duration(time.Duration(x * float64(time.Second)))
	default:
		return fmt.Errorf("duration must be a string like \"30s\"")
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
