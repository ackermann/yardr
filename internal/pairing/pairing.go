// Package pairing is the paths of a pairing's three requests, under the
// peers' listener (docs/peers.md, "Pairing"). The codes, messages and
// checks crossed on them live in internal/store, where the host's side
// uses them.
package pairing

// The paths of the three requests, under the peers' listener. Confirm is
// signed with the joiner's key, as a request between peers is.
const (
	PathHello   = "/peers/pair/hello"
	PathReveal  = "/peers/pair/reveal"
	PathConfirm = "/peers/pair/confirm"
)
