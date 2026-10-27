package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
)

// hostedMintFor is the hosted-Google minter the server registers on its
// store (store.SetHostedMint): one signed mint call through the edge for
// account, by the yard's key and the edge setting this yard pinned, with
// the 3.2 scope check before the token is held — an ungranted source's
// token is discarded with the answer, as the setup's probe discards its.
// The token goes back to the mint cache alone: it is printed nowhere,
// logged nowhere and kept in no event. The edge is called as the CLI's
// probe calls it (edgelink.MintGoogleThrough), with the system's roots.
func hostedMintFor(st store.API, c *config.Config) store.HostedMint {
	return func(ctx context.Context, addr, source, account string) (string, time.Time, error) {
		if err := edgelink.CheckGoogleMintSource(source); err != nil {
			return "", time.Time{}, err
		}
		mint, err := edgelink.MintGoogleThrough(ctx, st, c.PeerKey(), nil, addr, account)
		if err != nil {
			return "", time.Time{}, err
		}
		if err := edgelink.CheckGoogleScopes(source, addr, mint.GrantedScopes); err != nil {
			return "", time.Time{}, err
		}
		expires, err := time.Parse(time.RFC3339, strings.TrimSpace(mint.ExpiresAt))
		if err != nil {
			return "", time.Time{}, fmt.Errorf("the hosted mint's answer holds no expiry yardr reads (%q): expires_at is a time as RFC 3339 writes one", mint.ExpiresAt)
		}
		return mint.Token, expires, nil
	}
}
