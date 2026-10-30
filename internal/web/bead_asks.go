package web

import (
	"context"
	"errors"

	"yardr.dev/yardr/internal/store"
)

// loadBeadPageAsks puts the asks the request's user may answer and any held
// identity messages they may read on the bead page.
func (s *Server) loadBeadPageAsks(ctx context.Context, page *BeadPage, bead, actor string) error {
	asks, err := s.openBeadAsks(ctx, bead)
	if err != nil {
		return err
	}
	page.Asks = asks
	if len(asks) > 0 {
		site, err := s.cardSite(ctx)
		if err != nil {
			return err
		}
		page.site = site
	}
	for _, ask := range asks {
		if ask.Identity == nil {
			continue
		}
		payloads, err := s.st.IdentityHeldPayloads(ctx, bead, ask.Ask, actor)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if len(payloads) > 0 {
			page.HeldPayloads = map[uint64][]string{ask.Ask: payloads}
		}
		break
	}
	return nil
}
