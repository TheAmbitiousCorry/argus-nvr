package manager

import (
	"context"
	"log"
	"time"

	"argus-nvr/internal/store"
)

// AddressResolver turns the address a camera was configured under into one that
// this machine can actually reach. Discovery implements it.
type AddressResolver interface {
	ResolveAddress(addr string) (string, bool)
}

// WatchAddresses keeps stored addresses reachable. A camera added under the
// name its own discovery reported comes back offline inside the container,
// because the image has no mDNS resolver, and the same camera moves to a new
// address whenever DHCP decides it should. Discovery learns the IP alongside
// the name in both cases, so the stored address is rewritten to the one that
// resolves and the device is restarted against it.
func WatchAddresses(ctx context.Context, st *store.Store, mgr *Manager, r AddressResolver, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if resolveAddresses(st, r) {
			mgr.Sync(st.List())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// resolveAddresses rewrites every stored address discovery can improve on, and
// reports whether any changed.
func resolveAddresses(st *store.Store, r AddressResolver) bool {
	changed := false
	for _, c := range st.List() {
		addr, ok := r.ResolveAddress(c.Address)
		if !ok {
			continue
		}
		moved, err := st.SetAddress(c.ID, addr)
		if err != nil {
			log.Printf("camera %s: could not store address %s: %v", c.Name, addr, err)
			continue
		}
		if moved {
			log.Printf("camera %s: %s resolves to %s, using that", c.Name, c.Address, addr)
			changed = true
		}
	}
	return changed
}
