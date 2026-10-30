// How a bead answer reads the values a secret ask asks for: unseen from
// a terminal, or as NAME=value lines from anything else. A waiting
// login's inputs, a hook's signing secret and a harness kind's API key
// each read their own way, against the names the ask holds, and no
// error holds a value.
package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// readLoginKeys reads the values a login-input secret ask asks for, as
// readKeys reads a source's: unseen from a terminal, or as NAME=value lines
// from anything else. There is no declaration to read them against: the ask
// names the one. No error holds a value.
func readLoginKeys(secret *store.AskSecret, in io.Reader, typed func(prompt string) (string, error)) (map[string]string, error) {
	values := map[string]string{}
	if typed == nil {
		env, err := store.ParseEnv(in, "standard input")
		if err != nil {
			return nil, err
		}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if !slices.Contains(secret.Env, k) {
				// The input is intended to be NAME=value lines, but do not
				// repeat an unexpected name: a caller may have put the
				// secret there by mistake.
				return nil, fmt.Errorf("the waiting login %s: standard input sets a name that was not asked for (%s)", secret.Login, strings.Join(secret.Env, ", "))
			}
			values[k] = v
		}
		for _, n := range secret.Env {
			if _, ok := values[n]; !ok {
				return nil, fmt.Errorf("the waiting login %s: standard input has no line %s=<value>", secret.Login, n)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("the waiting login %s: standard input has no NAME=value line, and nothing is handed over", secret.Login)
		}
		return values, nil
	}
	for _, n := range secret.Env {
		v, err := typed(fmt.Sprintf("%s for the waiting login %s (nothing is shown as you type): ", n, secret.Login))
		if err != nil {
			return nil, fmt.Errorf("the waiting login %s: reading %s: %w", secret.Login, n, err)
		}
		// What a paste brings along at its ends is no part of an input.
		if v = strings.TrimSpace(v); v != "" {
			values[n] = v
		} else {
			return nil, fmt.Errorf("the waiting login %s: nothing was typed for %s, and nothing is handed over", secret.Login, n)
		}
	}
	return values, nil
}

// readHookKeys reads the value a hook-signing-secret ask asks for, as
// readLoginKeys reads a login's: unseen from a terminal, or as a
// NAME=value line from anything else. The ask names the one name. No
// error holds the value.
func readHookKeys(secret *store.AskSecret, in io.Reader, typed func(prompt string) (string, error)) (map[string]string, error) {
	values := map[string]string{}
	if typed == nil {
		env, err := store.ParseEnv(in, "standard input")
		if err != nil {
			return nil, err
		}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if !slices.Contains(secret.Env, k) {
				return nil, fmt.Errorf("the hook %s: standard input sets a name that was not asked for (%s)", secret.Hook, strings.Join(secret.Env, ", "))
			}
			values[k] = v
		}
		for _, n := range secret.Env {
			if _, ok := values[n]; !ok {
				return nil, fmt.Errorf("the hook %s: standard input has no line %s=<value>", secret.Hook, n)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("the hook %s: standard input has no NAME=value line, and nothing is written", secret.Hook)
		}
		return values, nil
	}
	for _, n := range secret.Env {
		v, err := typed(fmt.Sprintf("%s for the hook %s (nothing is shown as you type): ", n, secret.Hook))
		if err != nil {
			return nil, fmt.Errorf("the hook %s: reading %s: %w", secret.Hook, n, err)
		}
		if v = strings.TrimSpace(v); v != "" {
			values[n] = v
		} else {
			return nil, fmt.Errorf("the hook %s: nothing was typed for %s, and nothing is written", secret.Hook, n)
		}
	}
	return values, nil
}

// readHarnessKeys reads the values a harness-key secret ask asks for, as
// readHookKeys reads a hook's: unseen from a terminal, or as NAME=value
// lines from anything else. There is no declaration to read them against:
// the ask names them from the kind's harness file. No error holds a value.
func readHarnessKeys(secret *store.AskSecret, in io.Reader, typed func(prompt string) (string, error)) (map[string]string, error) {
	label := secret.Harness
	if h, ok := agents.Lookup(secret.Harness); ok && h.Label != "" {
		label = h.Label
	}
	values := map[string]string{}
	if typed == nil {
		env, err := store.ParseEnv(in, "standard input")
		if err != nil {
			return nil, err
		}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if !slices.Contains(secret.Env, k) {
				return nil, fmt.Errorf("the API key %s runs with: standard input sets a name that was not asked for (%s)", label, strings.Join(secret.Env, ", "))
			}
			values[k] = v
		}
		for _, n := range secret.Env {
			if _, ok := values[n]; !ok {
				return nil, fmt.Errorf("the API key %s runs with: standard input has no line %s=<value>", label, n)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("the API key %s runs with: standard input has no NAME=value line, and nothing is written", label)
		}
		return values, nil
	}
	for _, n := range secret.Env {
		v, err := typed(fmt.Sprintf("%s, the API key %s runs with (nothing is shown as you type): ", n, label))
		if err != nil {
			return nil, fmt.Errorf("the API key %s runs with: reading %s: %w", label, n, err)
		}
		if v = strings.TrimSpace(v); v != "" {
			values[n] = v
		} else {
			return nil, fmt.Errorf("the API key %s runs with: nothing was typed for %s, and nothing is written", label, n)
		}
	}
	return values, nil
}
