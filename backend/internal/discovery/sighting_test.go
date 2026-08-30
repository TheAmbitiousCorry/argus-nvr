package discovery

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func hdr(name string, rrtype uint16, ttl uint32) dns.RR_Header {
	return dns.RR_Header{Name: dns.Fqdn(name), Rrtype: rrtype, Class: dns.ClassINET, Ttl: ttl}
}

func answer(rrs ...dns.RR) *dns.Msg {
	m := new(dns.Msg)
	m.Answer = rrs
	return m
}

// The bug this package was rewritten for. A responder is entitled to answer an
// enumeration in pieces, and a real one on this network does: the pointer
// arrives in one packet and the service record in the next, with the address in
// neither. Anything that reads a packet on its own, cannot complete an entry
// and throws it away reports an empty network while every other tool on the
// same host lists the device.
func TestAnAnswerSpreadAcrossPacketsIsStillAnAnswer(t *testing.T) {
	s := newSighting()

	s.add(answer(&dns.PTR{
		Hdr: hdr(serviceName, dns.TypePTR, 4500),
		Ptr: dns.Fqdn("router._http._tcp.local"),
	}), serviceName)
	if got := s.hosts(); len(got) != 0 {
		t.Fatalf("a pointer on its own is not a host yet: %+v", got)
	}
	if got := s.unresolved(); len(got) != 0 {
		t.Fatalf("nothing has named a hostname yet, so nothing to ask about: %v", got)
	}

	s.add(answer(&dns.SRV{
		Hdr:    hdr("router._http._tcp.local", dns.TypeSRV, 4500),
		Port:   80,
		Target: dns.Fqdn("router.local"),
	}), serviceName)
	if got := s.unresolved(); len(got) != 1 || got[0] != "router.local" {
		t.Fatalf("unresolved = %v, want the hostname to ask about directly", got)
	}
	if got := s.hosts(); len(got) != 0 {
		t.Fatalf("still no address, so still not a host: %+v", got)
	}

	// The address, which this responder only ever sends when asked for it by
	// name, arrives in a third packet.
	s.add(answer(&dns.A{
		Hdr: hdr("router.local", dns.TypeA, 120),
		A:   []byte{192, 168, 10, 1},
	}), serviceName)

	got := s.hosts()
	if len(got) != 1 {
		t.Fatalf("assembled %d hosts, want one: %+v", len(got), got)
	}
	if got[0].Name != "router" || got[0].Host != "router.local" || got[0].IP != "192.168.10.1" || got[0].Port != 80 {
		t.Errorf("assembled %+v", got[0])
	}
	if left := s.unresolved(); len(left) != 0 {
		t.Errorf("still asking about %v after it answered", left)
	}
}

// The cameras answer with everything in one packet, in the additional section
// rather than the answer section. That has to keep working.
func TestACameraAnsweringInOnePacket(t *testing.T) {
	m := new(dns.Msg)
	m.Answer = []dns.RR{&dns.PTR{
		Hdr: hdr(serviceName, dns.TypePTR, 4500),
		Ptr: dns.Fqdn("camera-alpha._http._tcp.local"),
	}}
	// The cache-flush bit is set on the records a camera sends, which reads as
	// a class of 32769 rather than 1.
	m.Extra = []dns.RR{
		&dns.SRV{
			Hdr:    dns.RR_Header{Name: dns.Fqdn("camera-alpha._http._tcp.local"), Rrtype: dns.TypeSRV, Class: 32769, Ttl: 120},
			Port:   80,
			Target: dns.Fqdn("camera-alpha.local"),
		},
		&dns.A{
			Hdr: dns.RR_Header{Name: dns.Fqdn("camera-alpha.local"), Rrtype: dns.TypeA, Class: 32769, Ttl: 120},
			A:   []byte{192, 168, 10, 208},
		},
	}

	s := newSighting()
	s.add(m, serviceName)
	got := s.hosts()
	if len(got) != 1 || got[0].Name != "camera-alpha" || got[0].IP != "192.168.10.208" {
		t.Fatalf("found %+v", got)
	}
}

// A device leaving says so with a record that has no time left to live. It
// should not be offered as something to add.
func TestADeviceSayingGoodbyeIsNotOffered(t *testing.T) {
	s := newSighting()
	s.add(answer(
		&dns.PTR{Hdr: hdr(serviceName, dns.TypePTR, 0), Ptr: dns.Fqdn("beta._http._tcp.local")},
		&dns.SRV{Hdr: hdr("beta._http._tcp.local", dns.TypeSRV, 0), Port: 80, Target: dns.Fqdn("beta.local")},
		&dns.A{Hdr: hdr("beta.local", dns.TypeA, 120), A: []byte{192, 168, 10, 180}},
	), serviceName)

	if got := s.hosts(); len(got) != 0 {
		t.Errorf("a departing device is still being offered: %+v", got)
	}
	if !s.gone["beta._http._tcp.local"] {
		t.Errorf("goodbye not noted: %v", s.gone)
	}
}

// Records about some other service on the same network are not this service.
func TestSomebodyElsesServiceIsIgnored(t *testing.T) {
	s := newSighting()
	s.add(answer(
		&dns.PTR{Hdr: hdr("_ipp._tcp.local", dns.TypePTR, 4500), Ptr: dns.Fqdn("printer._ipp._tcp.local")},
		&dns.SRV{Hdr: hdr("printer._ipp._tcp.local", dns.TypeSRV, 4500), Port: 631, Target: dns.Fqdn("printer.local")},
		&dns.A{Hdr: hdr("printer.local", dns.TypeA, 120), A: []byte{192, 168, 10, 9}},
	), serviceName)

	if got := s.hosts(); len(got) != 0 {
		t.Errorf("offered something that is not an %s: %+v", serviceName, got)
	}
}

// A name the service already depends on is asked about by name on every sweep,
// so a camera configured under its mDNS name is reachable straight after a
// restart rather than only once an enumeration happens to carry its address.
func TestANameTheServiceKnowsIsAskedAboutDirectly(t *testing.T) {
	d := New()
	if _, moved := d.ResolveAddress("camera-alpha.local"); moved {
		t.Fatal("nothing has answered for that name yet, so it must not be guessed at")
	}

	want := d.questions(newSighting())
	var asked bool
	for _, q := range want {
		if q.Name == "camera-alpha.local." && q.Qtype == dns.TypeA {
			asked = true
		}
	}
	if !asked {
		t.Errorf("the next sweep does not ask about the name: %+v", want)
	}

	// Once something answers for the name, resolving it stops being a guess.
	d.mu.Lock()
	d.addrs["camera-alpha.local"] = address{ip: "192.168.10.208", seen: time.Now()}
	d.mu.Unlock()

	got, moved := d.ResolveAddress("camera-alpha.local:8080")
	if got != "192.168.10.208:8080" || !moved {
		t.Errorf("ResolveAddress = %q, %v; want the address the network answered with", got, moved)
	}
}

func TestQuestionsAreAskedOnceAndNotAboutAddresses(t *testing.T) {
	got := hostQuestions([]string{"beta.local", "BETA.local.", "192.168.10.1", "", "camera-alpha.local"})
	if len(got) != 2 {
		t.Fatalf("asked %d questions, want two: %+v", len(got), got)
	}
	if got[0].Name != "beta.local." || got[1].Name != "camera-alpha.local." {
		t.Errorf("asked %+v", got)
	}
}
