package hooks

import "yardr.dev/yardr/internal/store"

// Fielder is what an Adapter whose deliveries carry fields beyond the
// common ones (store.WhereKeys) adds: the further keys a subscription to
// the source may filter on.
type Fielder interface {
	// WhereKeys are the source's --where keys beyond store.WhereKeys. A
	// field the adapter sets that is in neither list fails
	// TestAdapterFieldsAreWhereKeys.
	WhereKeys() []string
}

// The store refuses a --where key a subscription may not filter on, but it
// cannot ask the adapters what their deliveries carry (they import it), so
// each adapter with fields of its own registers them here, as the fixed
// form of an issue is registered (store.IssueForm).
func init() {
	for name, ad := range Adapters {
		if f, ok := ad.(Fielder); ok {
			store.AddWhereKeys(name, f.WhereKeys())
		}
	}
}
