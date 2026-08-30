package discovery

import (
	"net"
	"strings"

	"github.com/miekg/dns"
)

// sighting is everything one browse window heard, assembled across packets
// rather than within them.
//
// That is the whole of the fix. A responder is free to answer an enumeration in
// pieces: the pointer in one packet and the service and address records in the
// next, or the address never at all until it is asked for by name. Anything
// that reads each packet on its own and throws away what it cannot complete
// sees a name with no address, drops it, and reports an empty network while the
// tools on the same host list every device on it.
type sighting struct {
	// services is keyed by the full instance name, which is the only thing
	// every record about one device agrees on.
	services map[string]*advert
	// addrs is every address record heard, keyed by hostname. A responder
	// answers for its own name whether or not it was asked about a service, so
	// this is also where a direct lookup of a name lands.
	addrs map[string]string
	// gone is instances that said goodbye, which is a record with no time left
	// to live.
	gone map[string]bool
}

type advert struct {
	instance string
	host     string
	port     int
	// txt is the service's own description of itself. Every web server on a
	// network advertises _http._tcp, so without this a browse for cameras finds
	// routers, printers and anything else with a status page, and cannot tell
	// which is which.
	txt []string
}

func newSighting() *sighting {
	return &sighting{
		services: make(map[string]*advert),
		addrs:    make(map[string]string),
		gone:     make(map[string]bool),
	}
}

// add folds one answer into what is already known. Every section is read, not
// just the answers: a responder puts the service and address records it thinks
// the asker will want next in the additional section, and that is where the
// cameras put theirs.
func (s *sighting) add(msg *dns.Msg, serviceName string) {
	if msg == nil {
		return
	}
	sections := make([]dns.RR, 0, len(msg.Answer)+len(msg.Ns)+len(msg.Extra))
	sections = append(sections, msg.Answer...)
	sections = append(sections, msg.Ns...)
	sections = append(sections, msg.Extra...)

	for _, rr := range sections {
		switch v := rr.(type) {
		case *dns.PTR:
			if !strings.EqualFold(v.Hdr.Name, serviceName) {
				continue
			}
			s.note(v.Ptr, serviceName, v.Hdr.Ttl)
		case *dns.SRV:
			if !isInstanceOf(v.Hdr.Name, serviceName) {
				continue
			}
			e := s.note(v.Hdr.Name, serviceName, v.Hdr.Ttl)
			if e != nil {
				e.host = trimDot(v.Target)
				e.port = int(v.Port)
			}
		case *dns.TXT:
			if !isInstanceOf(v.Hdr.Name, serviceName) {
				continue
			}
			if e := s.note(v.Hdr.Name, serviceName, v.Hdr.Ttl); e != nil {
				e.txt = append(e.txt, v.Txt...)
			}
		case *dns.A:
			ip := v.A.To4()
			if ip == nil || ip.IsUnspecified() {
				continue
			}
			if v.Hdr.Ttl == 0 {
				delete(s.addrs, hostKey(v.Hdr.Name))
				continue
			}
			s.addrs[hostKey(v.Hdr.Name)] = ip.String()
		}
	}
}

// note records that an instance exists, or that it has gone.
func (s *sighting) note(instance, serviceName string, ttl uint32) *advert {
	key := hostKey(instance)
	if ttl == 0 {
		// A goodbye. The device is leaving, so what was heard about it earlier
		// in this same window is no longer worth keeping.
		s.gone[key] = true
		delete(s.services, key)
		return nil
	}
	if s.gone[key] {
		return nil
	}
	e, ok := s.services[key]
	if !ok {
		e = &advert{instance: instanceLabel(instance, serviceName)}
		s.services[key] = e
	}
	return e
}

// unresolved is the hostnames that were named but never answered for. They are
// what a second round of questions asks about directly, which is what turns a
// responder that answers enumeration in pieces into a host with an address.
func (s *sighting) unresolved() []string {
	var out []string
	for _, e := range s.services {
		if e.host == "" {
			continue
		}
		if _, ok := s.addrs[hostKey(e.host)]; ok {
			continue
		}
		out = append(out, e.host)
	}
	return out
}

// hosts is what the window found: the instances that have both a name and an
// address. An instance with neither is not something the UI can offer to add.
func (s *sighting) hosts() []Host {
	out := make([]Host, 0, len(s.services))
	for _, e := range s.services {
		if e.host == "" {
			continue
		}
		ip, ok := s.addrs[hostKey(e.host)]
		if !ok {
			continue
		}
		out = append(out, Host{
			Name:     e.instance,
			Host:     trimDot(e.host),
			IP:       ip,
			Port:     e.port,
			Camera:   hasTag(e.txt, "argus", "cam"),
			Firmware: tagValue(e.txt, "fw"),
		})
	}
	return out
}

// isInstanceOf reports whether name is an instance of the service, rather than
// the service itself or something else entirely.
func isInstanceOf(name, serviceName string) bool {
	name, serviceName = strings.ToLower(name), strings.ToLower(serviceName)
	return len(name) > len(serviceName) && strings.HasSuffix(name, "."+serviceName)
}

// instanceLabel is the part of an instance name that names the device.
func instanceLabel(name, serviceName string) string {
	if isInstanceOf(name, serviceName) {
		return trimDot(name[:len(name)-len(serviceName)-1])
	}
	return trimDot(name)
}

// hostKey is how names are compared. mDNS names are case insensitive and are
// written with a trailing dot about half the time.
func hostKey(name string) string { return strings.ToLower(trimDot(name)) }

// questionFor builds one question, always in the internet class: the top bit of
// the class field asks for a unicast reply, and a browse wants the multicast
// one so that every listener on this host, including this one, hears it.
func questionFor(name string, qtype uint16) dns.Question {
	return dns.Question{Name: dns.Fqdn(name), Qtype: qtype, Qclass: dns.ClassINET}
}

// hostQuestions asks for the addresses of names already known about, so a
// device that has stopped answering enumeration is still found by the name the
// service is holding for it.
func hostQuestions(names []string) []dns.Question {
	seen := make(map[string]bool, len(names))
	out := make([]dns.Question, 0, len(names))
	for _, name := range names {
		key := hostKey(name)
		if key == "" || seen[key] || net.ParseIP(key) != nil {
			continue
		}
		seen[key] = true
		out = append(out, questionFor(name, dns.TypeA))
		if len(out) >= maxDirectQuestions {
			break
		}
	}
	return out
}

// hasTag reports whether the service described itself with the given key and
// value. This is what separates a camera from every other web server on the
// network.
func hasTag(txt []string, key, want string) bool {
	return strings.EqualFold(tagValue(txt, key), want)
}

// tagValue reads one key out of a TXT record, which is a list of "key=value"
// strings. A key with no value is not one we ask about, so a missing "=" is
// treated as absent rather than as an empty value.
func tagValue(txt []string, key string) string {
	for _, entry := range txt {
		k, v, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}
