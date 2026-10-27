package edgelink

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// GoogleMintSources are the sources the hosted mint mints for, with the
// Google scopes each needs from the connection's granted set (design
// yardr-hosted-4dx0 3.2): a source whose scope was not granted refuses at
// its mint, before any token is held.
var GoogleMintSources = []string{"google-drive", "google-docs", "google-slides", "google-calendar"}

var GoogleMintScopes = map[string][]string{
	"google-drive":    {"https://www.googleapis.com/auth/drive.file"},
	"google-docs":     {"https://www.googleapis.com/auth/documents"},
	"google-slides":   {"https://www.googleapis.com/auth/presentations"},
	"google-calendar": {"https://www.googleapis.com/auth/calendar.events", "https://www.googleapis.com/auth/calendar.calendarlist.readonly"},
}

// MissingGoogleScopes are the scopes source needs that granted does not
// hold, in the order GoogleMintScopes names them.
func MissingGoogleScopes(source string, granted []string) []string {
	var missing []string
	for _, need := range GoogleMintScopes[source] {
		if !slices.Contains(granted, need) {
			missing = append(missing, need)
		}
	}
	return missing
}

// CheckGoogleMintSource refuses a source that is not one the hosted
// Google mint serves. An unknown source has no scope row, and treating
// that as no missing scopes would hand it a Google token unchecked.
func CheckGoogleMintSource(source string) error {
	if _, ok := GoogleMintScopes[source]; !ok {
		return fmt.Errorf("source %s is not served by the hosted Google mint", source)
	}
	return nil
}

// MintGoogleThrough makes one signed mint call for account at the edge
// addr, as the hosted setup's probe and the server's in-process mint both
// make it: the yard's key loaded from peerKey, and the edge's key the one
// this yard pinned where its edge setting names addr, else the one a
// connect would fetch. The answer's token is the caller's to discard,
// check or hold; nothing here checks its scopes (MissingGoogleScopes).
func MintGoogleThrough(ctx context.Context, st store.API, peerKey string, hc *http.Client, addr, account string) (*GoogleMint, error) {
	yard, err := yardkey.Load(peerKey)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("this yard has no key, so it can sign nothing: connect it first (yardr edge connect <url> makes the pair)")
		}
		return nil, err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	client := &Client{URL: addr, HTTP: hc}
	// The key this yard pinned, where its edge setting names this edge;
	// else the key a connect would fetch. A pin is never replaced by a
	// later fetch.
	edgeKey := ""
	if e, connected, perr := store.EdgeOf(settings); perr == nil && connected && e.URL == addr {
		edgeKey = e.Key
	}
	if edgeKey == "" {
		if edgeKey, err = client.Key(ctx); err != nil {
			return nil, err
		}
	}
	return client.MintGoogle(ctx, edgeKey, yard, account)
}

// CheckGoogleScopes refuses a mint answer whose granted scopes miss what
// source needs, naming the source, the missing scope and where to
// reconnect: the 3.2 check every hosted mint makes before its token is
// held, whose words the setup's probe and the in-process mint share.
func CheckGoogleScopes(source, addr string, granted []string) error {
	if err := CheckGoogleMintSource(source); err != nil {
		return err
	}
	if missing := MissingGoogleScopes(source, granted); len(missing) > 0 {
		return fmt.Errorf("%s needs %s, missing from the Google connection's grant: reconnect at %s/google", source, strings.Join(missing, ", "), addr)
	}
	return nil
}
