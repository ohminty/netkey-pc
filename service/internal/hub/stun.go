package hub

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"sort"
	"strconv"
	"time"
)

const stunMagic = 0x2112A442

// STUNRTTs sends a few STUN Binding requests to every target ({key: "ip:port"}) from one socket, all at once, and
// returns the median round trip in ms per target that answered (like the box's hubclient.stun_rtts).
func STUNRTTs(targets map[string]string, tries int, timeout, gap time.Duration) map[string]float64 {
	c, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil
	}
	defer c.Close()
	type sent struct {
		key string
		at  time.Time
	}
	pending := map[[12]byte]sent{}
	samples := map[string][]float64{}
	addrs := map[string]*net.UDPAddr{}
	for k, a := range targets {
		if ua, err := net.ResolveUDPAddr("udp4", a); err == nil {
			addrs[k] = ua
		}
	}
	go func() {
		for i := 0; i < tries; i++ {
			for k, ua := range addrs {
				var pkt [20]byte
				binary.BigEndian.PutUint16(pkt[0:], 1) // Binding request
				binary.BigEndian.PutUint32(pkt[4:], stunMagic)
				rand.Read(pkt[8:20])
				var id [12]byte
				copy(id[:], pkt[8:20])
				pendingSet(pending, id, sent{k, time.Now()})
				_, _ = c.WriteToUDP(pkt[:], ua)
			}
			time.Sleep(gap)
		}
	}()
	end := time.Now().Add(gap*time.Duration(tries-1) + timeout)
	buf := make([]byte, 2048)
	for time.Now().Before(end) {
		_ = c.SetReadDeadline(end)
		n, _, err := c.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if n < 20 || buf[0] != 1 || buf[1] != 1 || binary.BigEndian.Uint32(buf[4:]) != stunMagic {
			continue
		}
		var id [12]byte
		copy(id[:], buf[8:20])
		if s, ok := pendingTake(pending, id); ok {
			samples[s.key] = append(samples[s.key], float64(time.Since(s.at).Microseconds())/1000)
		}
	}
	out := map[string]float64{}
	for k, v := range samples {
		sort.Float64s(v)
		out[k] = float64(int(v[len(v)/2]*10+0.5)) / 10
	}
	return out
}

var pendingMu = make(chan struct{}, 1)

func pendingSet[K comparable, V any](m map[K]V, k K, v V) {
	pendingMu <- struct{}{}
	m[k] = v
	<-pendingMu
}

func pendingTake[K comparable, V any](m map[K]V, k K) (V, bool) {
	pendingMu <- struct{}{}
	v, ok := m[k]
	delete(m, k)
	<-pendingMu
	return v, ok
}

// ProbeRelays measures every relay the hub lists and reports the times, so the hub picks good relays for this PC
// and the public party list can estimate the ping to each host.
func (c *Client) ProbeRelays(ctx context.Context) (map[string]float64, error) {
	var d struct {
		Relays []struct {
			ID       string `json:"id"`
			Host     string `json:"host"`
			STUNPort int    `json:"stun_port"`
		} `json:"relays"`
	}
	if err := c.Call(ctx, "GET", "/v1/relays", nil, &d); err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for i, r := range d.Relays {
		if i >= 128 || r.STUNPort <= 0 || r.STUNPort > 65535 || r.ID == "" {
			continue
		}
		host := r.Host
		if host == "" {
			host = c.host
		}
		targets[r.ID] = net.JoinHostPort(host, strconv.Itoa(r.STUNPort))
	}
	rtt := STUNRTTs(targets, 3, time.Second, 200*time.Millisecond)
	if len(rtt) > 0 {
		if err := c.Call(ctx, "POST", "/v1/rtt", map[string]any{"rtt": rtt}, nil); err != nil {
			return rtt, err
		}
	}
	return rtt, nil
}
