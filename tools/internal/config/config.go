// Package config binds command-line flags that default to environment
// variables, so the same binary is configurable from a Kubernetes Deployment
// (env) or locally (flags). Flags win over env.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// String defines a string flag defaulting to $env (or def).
func String(fs *flag.FlagSet, name, env, def, usage string) *string {
	if v, ok := os.LookupEnv(env); ok {
		def = v
	}
	return fs.String(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

// Int64 defines an int64 flag defaulting to $env (or def). An unparsable env
// value is reported by Validate-time callers through the returned error func.
func Int64(fs *flag.FlagSet, name, env string, def int64, usage string) *int64 {
	if v, ok := os.LookupEnv(env); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			def = n
		} else {
			fmt.Fprintf(os.Stderr, "warning: ignoring invalid %s=%q\n", env, v)
		}
	}
	return fs.Int64(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

// Bool defines a bool flag defaulting to $env (or def).
func Bool(fs *flag.FlagSet, name, env string, def bool, usage string) *bool {
	if v, ok := os.LookupEnv(env); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			def = b
		} else {
			fmt.Fprintf(os.Stderr, "warning: ignoring invalid %s=%q\n", env, v)
		}
	}
	return fs.Bool(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

// Duration defines a duration flag defaulting to $env (or def).
func Duration(fs *flag.FlagSet, name, env string, def time.Duration, usage string) *time.Duration {
	if v, ok := os.LookupEnv(env); ok {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			def = d
		} else {
			fmt.Fprintf(os.Stderr, "warning: ignoring invalid %s=%q\n", env, v)
		}
	}
	return fs.Duration(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

// List splits a comma-separated value, dropping blanks.
func List(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Required returns an error naming every empty value in pairs of
// (flag name, value).
func Required(pairs ...string) error {
	var missing []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if strings.TrimSpace(pairs[i+1]) == "" {
			missing = append(missing, "-"+pairs[i])
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	return nil
}
