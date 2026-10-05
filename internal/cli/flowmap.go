package cli

import (
	"context"

	"yardr.dev/yardr/internal/store"
)

// flowMap is the map of the flow governing bead id, for prime; nil when no
// flow governs it. next is what NextStages said of the bead.
func (a *app) flowMap(ctx context.Context, st store.API, id string, next *store.NextStages) (*store.FlowMap, error) {
	if next == nil || next.Flow == nil {
		return nil, nil
	}
	m, err := st.FlowMap(ctx, id)
	if err != nil || m == nil {
		return nil, err
	}
	// A store flow's stage files are files in the yard's home, which the
	// store does not read.
	if next.Source.InStore() {
		if err := m.HomeSummaries(a.home()); err != nil {
			return nil, err
		}
	}
	return m, nil
}
