package discovery

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

// mdnsPort and mdnsGroup are fixed by the protocol. Queries and answers both go
// to the group, so the socket has to be on that port rather than an ephemeral
// one: a responder answers where it was asked from, and that is here.
const mdnsPort = 5353

var mdnsGroup = net.IPv4(224, 0, 0, 251)

// mdnsTTL is what the protocol requires on the wire. A packet that leaves with
// anything else is one a strict responder is entitled to ignore.
const mdnsTTL = 255

// maxPacketBytes is what one read is given. mDNS is a datagram protocol and a
// response that does not fit is split by the responder rather than truncated
// here, but a large read costs nothing and losing a legal packet costs a
// camera.
const maxPacketBytes = 9000

// browser is one mDNS conversation: a socket, the interfaces worth having it
// on, and no memory of its own. What was heard is the caller's to keep, which
// is the whole point: an answer that arrives in pieces across several packets
// has to be assembled by something that outlives a single read.
type browser struct {
	conn   *ipv4.PacketConn
	ifaces []net.Interface
	buf    []byte

	// onIface is the set of interface indexes answers are accepted from, so a
	// response arriving over a virtual bridge is not recorded as something on
	// the LAN.
	onIface map[int]bool
}

// newBrowser opens the socket and joins the multicast group on every interface
// worth browsing.
//
// Binding the group address rather than the wildcard is what lets this run
// beside a system mDNS daemon: the address is a multicast one, so the runtime
// sets the socket up to share the port, which an ordinary bind on 5353 would
// not. Without that this fails to start on any machine already running avahi,
// which is most of them.
func newBrowser() (*browser, error) {
	ifaces := usableInterfaces()
	if len(ifaces) == 0 {
		return nil, errors.New("no interface can carry multicast")
	}

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: mdnsGroup, Port: mdnsPort})
	if err != nil {
		return nil, fmt.Errorf("listen on %d: %w", mdnsPort, err)
	}
	conn := ipv4.NewPacketConn(udp)
	// The interface a packet arrived on is the only way to tell an answer from
	// the LAN apart from one from a container bridge.
	if err := conn.SetControlMessage(ipv4.FlagInterface, true); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetMulticastTTL(mdnsTTL)

	b := &browser{conn: conn, buf: make([]byte, maxPacketBytes), onIface: make(map[int]bool)}
	var joined []net.Interface
	var failed []string
	for _, ifi := range ifaces {
		if err := conn.JoinGroup(&ifi, &net.UDPAddr{IP: mdnsGroup}); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", ifi.Name, err))
			continue
		}
		joined = append(joined, ifi)
		b.onIface[ifi.Index] = true
	}
	if len(joined) == 0 {
		conn.Close()
		return nil, fmt.Errorf("could not join the multicast group on any of %s: %s",
			interfaceNames(ifaces), strings.Join(failed, ", "))
	}
	b.ifaces = joined
	return b, nil
}

func (b *browser) close() {
	if b.conn != nil {
		b.conn.Close()
	}
}

// ask sends one query out of every interface the group was joined on.
//
// Every interface, not one: a machine running this has a wireless card, a
// bridge for containers and whatever else virtualisation left behind, and
// picking wrongly between them is a browse that hears nothing while the tools
// on the same host find everything. A query on an interface with nothing on it
// costs one packet.
func (b *browser) ask(questions ...dns.Question) error {
	if len(questions) == 0 {
		return nil
	}
	msg := new(dns.Msg)
	msg.Question = questions
	msg.RecursionDesired = false
	wire, err := msg.Pack()
	if err != nil {
		return err
	}

	to := &net.UDPAddr{IP: mdnsGroup, Port: mdnsPort}
	var sent int
	var last error
	for _, ifi := range b.ifaces {
		cm := ipv4.ControlMessage{IfIndex: ifi.Index}
		if _, err := b.conn.WriteTo(wire, &cm, to); err != nil {
			last = err
			continue
		}
		sent++
	}
	if sent == 0 {
		return fmt.Errorf("no query left %s: %w", interfaceNames(b.ifaces), last)
	}
	return nil
}

// read waits for one answer, up to until. A read that times out returns nil
// rather than an error, because a window with nothing in it is the ordinary
// case on a quiet network rather than a fault.
func (b *browser) read(until time.Time) (*dns.Msg, error) {
	for {
		if err := b.conn.SetReadDeadline(until); err != nil {
			return nil, err
		}
		n, cm, _, err := b.conn.ReadFrom(b.buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return nil, nil
			}
			return nil, err
		}
		if cm != nil && !b.onIface[cm.IfIndex] {
			continue
		}
		msg := new(dns.Msg)
		if msg.Unpack(b.buf[:n]) != nil {
			continue
		}
		// Everything on this port that carries no answer is somebody else's
		// question, including the ones this process just asked.
		if len(msg.Answer) == 0 && len(msg.Extra) == 0 {
			continue
		}
		return msg, nil
	}
}

// usableInterfaces picks the interfaces a browse has any chance on.
//
// Up and multicast-capable is not enough. A machine running this has a docker
// bridge and often a spare wired port, both of which are administratively up,
// both of which advertise multicast, and neither of which has a cable or a
// container behind it. Requiring a carrier and an IPv4 address is what leaves
// the interface the cameras are actually on.
func usableInterfaces() []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out, fallback []net.Interface
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if !hasIPv4(ifi) {
			continue
		}
		fallback = append(fallback, ifi)
		if ifi.Flags&net.FlagRunning == 0 {
			continue
		}
		out = append(out, ifi)
	}
	// A host where nothing reports a carrier is one this cannot second-guess,
	// so every candidate is tried rather than none.
	if len(out) == 0 {
		return fallback
	}
	return out
}

func hasIPv4(ifi net.Interface) bool {
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			return true
		}
	}
	return false
}

func interfaceNames(ifaces []net.Interface) string {
	names := make([]string, 0, len(ifaces))
	for _, ifi := range ifaces {
		names = append(names, ifi.Name)
	}
	if len(names) == 0 {
		return "no interfaces"
	}
	return strings.Join(names, ", ")
}
