// Package telemetry is yardr's opt-in telemetry: the one switch, the
// install id, and the daily batch of counts and fixed names the daemon
// sends (docs/design/2026-10-28-yardr-fwhv-telemetry.md). The switch is
// the telemetry setting, the id and its consent bookkeeping live in
// <home>/telemetry.json, Build makes the batch from the store's log, and
// On, Off and Show are the switch behind yardr telemetry on, off and
// show. Nothing here sends: the daemon's sender comes later.
package telemetry

import (
	"time"
)

// Endpoint is where the daily batch goes: the shared EU project's batch
// capture endpoint. It is compiled in, never configured, so no config
// can point a yard's telemetry elsewhere.
const Endpoint = "https://eu.i.posthog.com/batch/"

// APIKey is the shared EU project's public, write-only key. It is
// compiled in, never configured, and is part of the request show previews.
const APIKey = "phc_s9ANvMv9vKG3FLfuGFHLQQWPdHsyqv3fqC6hkcFgNkBA"

// IDSentence is said wherever the install id is shown: the id is
// pseudonymous, and sharing it is the user's act.
const IDSentence = "This id is pseudonymous, not anonymous: anyone you give it to can find this yard's events, and giving it is your act."

// dayLayout is a UTC day as the state file and the builder hold it.
const dayLayout = "2006-01-02"

// stampLayout is a UTC timestamp as the state file holds it, to the
// second: event day markers built from it compare equal.
const stampLayout = "2006-01-02T15:04:05Z"

// dayOf is the UTC day of t.
func dayOf(t time.Time) string { return t.UTC().Format(dayLayout) }

// stampOf is t as the state file holds it: UTC, to the second.
func stampOf(t time.Time) string { return t.UTC().Truncate(time.Second).Format(stampLayout) }

// parseDay reads a UTC day as dayOf wrote it.
func parseDay(s string) (time.Time, error) { return time.Parse(dayLayout, s) }
