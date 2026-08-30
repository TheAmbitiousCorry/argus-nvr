package discovery

import (
	"testing"
	"time"
)

// The container has no mDNS resolver, so a camera added under the name its own
// discovery reported is unreachable until the address is swapped for the IP the
// browse already carried alongside it.
func TestResolveAddress(t *testing.T) {
	d := New()
	d.hosts["192.168.10.208"] = Host{
		Name: "camera-alpha",
		Host: "camera-alpha.local",
		IP:   "192.168.10.208",
		Port: 80,
		Seen: time.Now(),
	}

	tests := []struct {
		name      string
		addr      string
		want      string
		wantMoved bool
	}{
		{
			name:      "the mDNS hostname",
			addr:      "camera-alpha.local",
			want:      "192.168.10.208",
			wantMoved: true,
		},
		{
			name:      "the instance name on its own",
			addr:      "camera-alpha",
			want:      "192.168.10.208",
			wantMoved: true,
		},
		{
			name:      "case does not matter to mDNS",
			addr:      "Camera-Alpha.local",
			want:      "192.168.10.208",
			wantMoved: true,
		},
		{
			name:      "a port is kept",
			addr:      "camera-alpha.local:8080",
			want:      "192.168.10.208:8080",
			wantMoved: true,
		},
		{
			name: "an address added by IP is left exactly as it was",
			addr: "192.168.10.180",
			want: "192.168.10.180",
		},
		{
			name: "an IP with a port is left alone too",
			addr: "192.168.10.180:80",
			want: "192.168.10.180:80",
		},
		{
			name: "a name nothing has answered for is not guessed at",
			addr: "camera-delta.local",
			want: "camera-delta.local",
		},
		{
			name: "the IP this host was found at is already right",
			addr: "192.168.10.208",
			want: "192.168.10.208",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, moved := d.ResolveAddress(tt.addr)
			if got != tt.want || moved != tt.wantMoved {
				t.Errorf("ResolveAddress(%q) = %q, %v; want %q, %v", tt.addr, got, moved, tt.want, tt.wantMoved)
			}
		})
	}
}
