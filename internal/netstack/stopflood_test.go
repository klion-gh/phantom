package netstack

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"phantom/internal/protocol"
	"phantom/internal/tunnel"
)

// The field failure behind "switching configs took the whole internet down":
// Stop never returned while the device kept opening connections - a phone
// in smart mode opens dozens a minute. Destroy aborts the connections that
// exist when it starts, then waits for every connection to finish closing;
// one accepted after that sweep was closed gracefully (its tunnel stream
// failed with "session closed") by a stack whose TCP processing had already
// been shut down, so it never finished closing and Destroy waited forever.
// Meanwhile the VPN interface stayed up, feeding the whole device's traffic
// into a stack that could no longer carry any of it.
//
// The bounded wait in Stop is switched off here: this checks the teardown
// itself finishes, not that Stop gives up on it.
func TestStopReturnsWhileTheDeviceKeepsOpeningConnections(t *testing.T) {
	defer func(d time.Duration) { stopAbandonAfter = d }(stopAbandonAfter)
	stopAbandonAfter = time.Hour
	for round := 0; round < 5; round++ {
		stopUnderConnectionFlood(t)
	}
}

func stopUnderConnectionFlood(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()
	go io.Copy(io.Discard, c2) // a server that accepts whatever is written
	crypto, err := protocol.DeriveSessionKeys(
		[]byte("0123456789abcdef0123456789abcdef"), []byte("shared-psk-bytes-1234567890abcdef"),
		[]byte("client-ephemeral-pub-1234567890ab"), []byte("server-static-pub-1234567890abcd"),
		protocol.RoleClient)
	if err != nil {
		t.Fatal(err)
	}
	session := tunnel.NewSessionFromMux(tunnel.NewMultiplexer(c1, crypto))

	tunLink := channel.New(512, 1500, "")
	tun, err := New(session, tunLink, 1500)
	if err != nil {
		t.Fatal(err)
	}
	devLink := channel.New(512, 1500, "")
	dev := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	defer dev.Destroy()
	if e := dev.CreateNIC(1, devLink); e != nil {
		t.Fatal(e)
	}
	dev.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4([4]byte{10, 10, 0, 2}).WithPrefix(),
	}, stack.AddressProperties{})
	dev.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})

	pumpCtx, stopPump := context.WithCancel(context.Background())
	defer stopPump()
	pump := func(from, to *channel.Endpoint) {
		for {
			pkt := from.ReadContext(pumpCtx)
			if pkt == nil {
				return
			}
			data := append([]byte(nil), pkt.ToView().AsSlice()...)
			pkt.DecRef()
			in := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(data)})
			to.InjectInbound(ipv4.ProtocolNumber, in)
			in.DecRef()
		}
	}
	go pump(devLink, tunLink)
	go pump(tunLink, devLink)

	// The device: several apps opening connections non-stop, before, during
	// and after the stop.
	floodCtx, stopFlood := context.WithCancel(context.Background())
	var flood sync.WaitGroup
	for w := 0; w < 8; w++ {
		flood.Add(1)
		go func(w int) {
			defer flood.Done()
			for port := uint16(1000 + w*1000); floodCtx.Err() == nil; port++ {
				dialCtx, cancel := context.WithTimeout(floodCtx, 300*time.Millisecond)
				c, err := gonet.DialContextTCP(dialCtx, dev, tcpip.FullAddress{
					NIC: 1, Addr: tcpip.AddrFrom4([4]byte{203, 0, 113, byte(10 + w)}), Port: port,
				}, ipv4.ProtocolNumber)
				cancel()
				if err == nil {
					c.Close()
				}
			}
		}(w)
	}
	defer func() {
		stopFlood()
		flood.Wait()
	}()
	time.Sleep(100 * time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		tun.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung while the device kept opening connections")
	}
}
