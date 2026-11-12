package upnptest

import (
	"net"
	"strings"
)

// ServeSSDP starts answering searches on a loopback UDP port and returns
// its address, for an ssdp.Options.Target. A real plug answers only a
// search that arrived on the multicast group; this one answers any, so a
// test needs no multicast. Every search gets one answer, as a plug gives
// one per search target asked.
func (p *Plug) ServeSSDP() string {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		panic(err)
	}

	p.ssdp = conn
	p.ssdpAddr = conn.LocalAddr().String()

	go p.answerSearches(conn)

	return p.ssdpAddr
}

// searchSize is more than any M-SEARCH.
const searchSize = 2048

func (p *Plug) answerSearches(conn *net.UDPConn) {
	buffer := make([]byte, searchSize)

	for {
		count, from, err := conn.ReadFrom(buffer)
		if err != nil {
			return
		}

		request := string(buffer[:count])
		if !strings.HasPrefix(request, "M-SEARCH") {
			continue
		}

		p.Searches.Add(1)

		target := "upnp:rootdevice"

		for line := range strings.SplitSeq(request, "\r\n") {
			if key, value, found := strings.Cut(line, ":"); found && strings.EqualFold(strings.TrimSpace(key), "ST") {
				target = strings.TrimSpace(value)
			}
		}

		answer := "HTTP/1.1 200 OK\r\n" +
			"CACHE-CONTROL: max-age=86400\r\n" +
			"DATE: Tue, 11 Nov 2026 00:00:00 GMT\r\n" +
			"EXT:\r\n" +
			"LOCATION: " + p.Location() + "\r\n" +
			"OPT: \"http://schemas.upnp.org/upnp/1/0/\"; ns=01\r\n" +
			"01-NLS: 1\r\n" +
			"SERVER: Unspecified, UPnP/1.0, Unspecified\r\n" +
			"X-User-Agent: redsonic\r\n" +
			"ST: " + target + "\r\n" +
			"USN: " + UDN + "::" + target + "\r\n\r\n"

		_, _ = conn.WriteTo([]byte(answer), from)
	}
}
