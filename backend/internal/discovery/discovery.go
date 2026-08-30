// Package discovery browses mDNS for HTTP services on the LAN so the UI can
// offer to add cameras that are not configured yet.
package discovery

import (
	"context"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	service = "_http._tcp"
	domain  = "local."

	// browseWindow is how long each browse listens for responses, and
	// browseEvery how often a fresh sweep starts. Discovery is a convenience,
	// so it sweeps gently rather than continuously.
	browseWindow = 5 * time.Second
	browseEvery  = 30 * time.Second

	// entryTTL drops hosts that have stopped answering, so the UI does not
	// keep offering a camera that has been unplugged.
	entryTTL = 3 * browseEvery
)

// Host is one mDNS responder. Cameras advertise an empty TXT record, and other
// devices such as routers advertise _http._tcp too, so nothing here can be
// assumed to be a camera; the UI presents these as candidates to add.
type Host struct {
	Name string    `json:"name"`
	Host string    `json:"host"`
	IP   string    `json:"ip"`
	Port int       `json:"port"`
	Seen time.Time `json:"seen"`
}

// Discoverer keeps a rolling view of what is on the network.
type Discoverer struct {
	mu    sync.RWMutex
	hosts map[string]Host
}

// New returns a Discoverer that has not started browsing yet.
func New() *Discoverer {
	return &Discoverer{hosts: make(map[string]Host)}
}

// Run browses until ctx is cancelled. Every failure is logged and retried
// rather than returned: a machine with no network, or one where mDNS is
// blocked, must still start and serve cameras by IP.
func (d *Discoverer) Run(ctx context.Context) {
	for {
		if err := d.browse(ctx); err != nil && ctx.Err() == nil {
			log.Printf("discovery: %v", err)
		}
		d.expire()
		select {
		case <-ctx.Done():
			return
		case <-time.After(browseEvery):
		}
	}
}

func (d *Discoverer) browse(ctx context.Context) error {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return err
	}

	entries := make(chan *zeroconf.ServiceEntry, 16)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for e := range entries {
			d.record(e)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, browseWindow)
	defer cancel()

	// Browse closes entries when the context expires, so the collector above
	// ends on its own.
	err = resolver.Browse(ctx, service, domain, entries)
	if err != nil {
		cancel()
		wg.Wait()
		return err
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (d *Discoverer) record(e *zeroconf.ServiceEntry) {
	if e == nil {
		return
	}
	ip := firstIPv4(e.AddrIPv4)
	// An entry that never resolved to an address is useless to the UI, and the
	// cameras do intermittently answer the browse without resolving.
	if ip == "" {
		return
	}
	h := Host{
		Name: e.Instance,
		Host: trimDot(e.HostName),
		IP:   ip,
		Port: e.Port,
		Seen: time.Now(),
	}
	d.mu.Lock()
	d.hosts[ip] = h
	d.mu.Unlock()
}

// Unconfigured returns discovered hosts whose address does not already belong
// to a configured camera, matching on both IP and mDNS hostname because a
// camera may have been added either way.
func (d *Discoverer) Unconfigured(known []string) []Host {
	taken := make(map[string]struct{}, len(known)*2)
	for _, k := range known {
		taken[normalise(k)] = struct{}{}
	}

	d.mu.RLock()
	out := make([]Host, 0, len(d.hosts))
	for _, h := range d.hosts {
		if _, ok := taken[normalise(h.IP)]; ok {
			continue
		}
		if _, ok := taken[normalise(h.Host)]; ok {
			continue
		}
		out = append(out, h)
	}
	d.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (d *Discoverer) expire() {
	cutoff := time.Now().Add(-entryTTL)
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, h := range d.hosts {
		if h.Seen.Before(cutoff) {
			delete(d.hosts, k)
		}
	}
}

func firstIPv4(addrs []net.IP) string {
	for _, ip := range addrs {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

func trimDot(s string) string {
	for len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

// normalise reduces an address to a comparable host, ignoring any port and the
// trailing dot mDNS puts on names.
func normalise(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	return trimDot(addr)
}
