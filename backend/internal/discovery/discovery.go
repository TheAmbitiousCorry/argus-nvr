// Package discovery browses mDNS for HTTP services on the LAN so the UI can
// offer to add cameras that are not configured yet, and so a camera configured
// under a name has an address the container can actually reach.
//
// It speaks the protocol directly rather than through a browse library, for one
// reason: a responder may answer an enumeration across several packets, or
// answer the pointer and never volunteer the address at all. What is heard has
// to be assembled over a window and then asked about by name, and a library
// that hands over only the entries it could complete inside a single packet
// cannot be made to do that from the outside.
package discovery

import (
	"context"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
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

	// askAgainAfter is how long a window waits before repeating itself. The
	// first query catches a responder that is listening; the repeats catch one
	// that was busy, and carry the direct questions about whatever the first
	// round left half known. Each wait is twice the last, which is what the
	// protocol asks of a querier that keeps asking.
	askAgainAfter = time.Second

	// maxDirectQuestions bounds one query. A fleet is a dozen cameras and a
	// question is a few dozen bytes, so this is far past what a real network
	// needs and still keeps one packet under any path's limit.
	maxDirectQuestions = 32
)

// serviceName is the enumeration this browses for, as it goes on the wire.
var serviceName = dns.Fqdn(service + "." + trimDot(domain))

// Host is one mDNS responder. Every web server advertises _http._tcp, so a
// browse finds routers and printers alongside cameras; Camera is how they are
// told apart, and it is the camera's own claim rather than a guess from its
// name or address.
type Host struct {
	Name string `json:"name"`
	Host string `json:"host"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
	// Camera is true when the responder said so in its TXT record. A camera on
	// firmware old enough not to say is reported as not a camera, which is the
	// safe way round: offering a router as a camera is worse than making
	// someone type an address.
	Camera   bool      `json:"camera"`
	Firmware string    `json:"firmware,omitempty"`
	Seen     time.Time `json:"seen"`
}

// address is a name the network has answered for, whether or not the responder
// also advertised a service. It is what makes a camera configured under a name
// reachable straight after a restart, rather than only once an enumeration
// happens to carry both the name and the address in one packet.
type address struct {
	ip   string
	seen time.Time
}

// Discoverer keeps a rolling view of what is on the network.
type Discoverer struct {
	mu    sync.RWMutex
	hosts map[string]Host
	addrs map[string]address
	// asked is the names something has wanted an address for. Every sweep asks
	// the network about them directly, so a camera the service already knows
	// by name comes back on the first sweep after a restart instead of waiting
	// for an enumeration that may never carry its address.
	asked map[string]time.Time

	// on is the interfaces the last browse ran on, so a change is worth one
	// line and no change is worth none.
	on string
}

// New returns a Discoverer that has not started browsing yet.
func New() *Discoverer {
	return &Discoverer{
		hosts: make(map[string]Host),
		addrs: make(map[string]address),
		asked: make(map[string]time.Time),
	}
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

// browse runs one window: ask, listen, ask again about whatever is still half
// known, and record what came together by the end of it.
func (d *Discoverer) browse(ctx context.Context) error {
	b, err := newBrowser()
	if err != nil {
		return err
	}
	defer b.close()
	d.noteInterfaces(b.ifaces)

	heard := newSighting()
	deadline := time.Now().Add(browseWindow)

	if err := b.ask(d.questions(heard)...); err != nil {
		return err
	}
	wait := askAgainAfter
	nextAsk := time.Now().Add(wait)

	for ctx.Err() == nil {
		now := time.Now()
		if !now.Before(deadline) {
			break
		}
		if !now.Before(nextAsk) {
			// A failure to repeat is not a failure of the window: whatever the
			// first round heard is still worth keeping.
			b.ask(d.questions(heard)...)
			wait *= 2
			nextAsk = now.Add(wait)
		}
		until := deadline
		if nextAsk.Before(until) {
			until = nextAsk
		}
		msg, err := b.read(until)
		if err != nil {
			return err
		}
		heard.add(msg, serviceName)
	}

	d.record(heard)
	return nil
}

// questions is what to ask now: the enumeration itself, plus the addresses of
// every name that is known but unanswered for. Both go in one packet, because
// one packet is one chance for a microcontroller to be listening.
func (d *Discoverer) questions(heard *sighting) []dns.Question {
	names := heard.unresolved()
	names = append(names, d.wanted()...)
	return append([]dns.Question{questionFor(serviceName, dns.TypePTR)}, hostQuestions(names)...)
}

// wanted is the names worth asking about by name: everything a host has been
// seen at, and everything anything has asked to resolve.
func (d *Discoverer) wanted() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]string, 0, len(d.hosts)+len(d.asked))
	for _, h := range d.hosts {
		if h.Host != "" {
			out = append(out, h.Host)
		}
	}
	for name := range d.asked {
		out = append(out, name)
	}
	return out
}

// record folds a window's findings into the rolling view.
func (d *Discoverer) record(heard *sighting) {
	now := time.Now()
	hosts := heard.hosts()

	var found []Host
	d.mu.Lock()
	for _, h := range hosts {
		h.Seen = now
		if _, known := d.hosts[h.IP]; !known {
			found = append(found, h)
		}
		d.hosts[h.IP] = h
	}
	for name, ip := range heard.addrs {
		d.addrs[name] = address{ip: ip, seen: now}
	}
	// A device that said goodbye is gone now rather than in ninety seconds.
	for key := range heard.gone {
		for ip, h := range d.hosts {
			if hostKey(h.Name) == key || hostKey(h.Host) == key {
				delete(d.hosts, ip)
			}
		}
	}
	d.mu.Unlock()

	// One line the first time something answers, and none after. A browse that
	// says nothing for an hour, on a network the operator can see devices on
	// with any other tool, is the report this package exists to make useful.
	for _, h := range found {
		log.Printf("discovery: %s (%s) answers at %s", h.Name, h.Host, h.IP)
	}
}

// noteInterfaces says once which interfaces a browse is running on. On a
// machine with a wireless card, a docker bridge and whatever virtualisation
// left behind, this is the first thing worth knowing when nothing is found.
func (d *Discoverer) noteInterfaces(ifaces []net.Interface) {
	names := interfaceNames(ifaces)
	d.mu.Lock()
	changed := d.on != names
	d.on = names
	d.mu.Unlock()
	if changed {
		log.Printf("discovery: browsing on %s", names)
	}
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

// ResolveAddress rewrites an address so that it points at the IP discovery saw,
// keeping any port. A literal IP is returned untouched, so a camera added by
// address is never second-guessed, and an address no browse has matched is left
// alone rather than guessed at. The second return value reports whether the
// address changed.
//
// A name this cannot answer for yet is remembered, and the next sweep asks the
// network about it directly. That is what makes a camera configured under its
// mDNS name reachable after a restart: the answer no longer has to arrive
// inside an enumeration nobody controls the timing of.
func (d *Discoverer) ResolveAddress(addr string) (string, bool) {
	host, port := addr, ""
	if h, p, err := net.SplitHostPort(addr); err == nil {
		host, port = h, p
	}
	if host == "" || net.ParseIP(host) != nil {
		return addr, false
	}
	d.want(host)

	ip, ok := d.resolve(host)
	if !ok || ip == host {
		return addr, false
	}
	if port != "" {
		return net.JoinHostPort(ip, port), true
	}
	return ip, true
}

// want remembers a name worth asking the network about by name.
func (d *Discoverer) want(host string) {
	key := hostKey(host)
	if key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.asked) >= maxDirectQuestions {
		if _, ok := d.asked[key]; !ok {
			return
		}
	}
	d.asked[key] = time.Now()
}

// resolve returns the IPv4 address discovery saw for a host, given either its
// mDNS hostname (camera-alpha.local), its instance name (camera-alpha), or the
// IP itself. The container this service runs in has no mDNS resolver, so a name
// that the network answers for is still a name Go cannot look up; what the
// browse heard is the only place that translation exists.
func (d *Discoverer) resolve(name string) (string, bool) {
	want := normalise(name)
	if want == "" {
		return "", false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, h := range d.hosts {
		for _, candidate := range []string{h.IP, h.Host, h.Name, h.Name + "." + trimDot(domain)} {
			if strings.EqualFold(normalise(candidate), want) {
				return h.IP, true
			}
		}
	}
	// Nothing advertised a service under that name, but something may still
	// have answered for the name itself.
	for _, candidate := range []string{want, want + "." + trimDot(domain)} {
		if a, ok := d.addrs[hostKey(candidate)]; ok {
			return a.ip, true
		}
	}
	return "", false
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
	for k, a := range d.addrs {
		if a.seen.Before(cutoff) {
			delete(d.addrs, k)
		}
	}
	// A name nothing has asked about for a while is a camera that has been
	// removed, and is not worth a question every thirty seconds forever.
	for k, at := range d.asked {
		if at.Before(cutoff) {
			delete(d.asked, k)
		}
	}
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
