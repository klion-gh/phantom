package netstack

import (
	"context"
	"net"
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

// The field failure: the server went silent (a blackholed connection - the
// client can't get anything written), and the phone lost *all* internet, not
// just the listed sites - then a config switch or disconnect hung too.
//
// gVisor calls the UDP forwarder's handler inline, on the one goroutine that
// processes every packet the device sends. The handler opened the flow's
// tunnel stream right there, and opening a stream waits for the multiplexer
// to write the OPEN frame - which, with the server silent, it never does. One
// new UDP flow through the tunnel (QUIC to a listed site, a Telegram call)
// was enough to stop the device's packet processing entirely, direct traffic
// included, for as long as the server stayed silent.
//
// Here: a "device" stack wired to the tunnel like a TUN device, a server that
// never reads, a UDP datagram to a tunnelled destination - after which the
// device must still be able to open a TCP connection (i.e. packets are still
// being processed), and the tunnel must still stop.
func TestSilentServerDoesNotFreezePacketProcessing(t *testing.T) {
	// The session's connection goes nowhere: nothing ever reads c2, so the
	// first frame the multiplexer writes blocks, exactly like a blackholed
	// TCP connection whose send buffer has filled.
	c1, c2 := net.Pipe()
	defer c2.Close()
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
			// For the device->tunnel direction this call runs the tunnel's
			// packet processing - the same role as the fd dispatcher on
			// Android. If it blocks, nothing else the device sends is seen.
			to.InjectInbound(ipv4.ProtocolNumber, in)
			in.DecRef()
		}
	}
	go pump(devLink, tunLink)
	go pump(tunLink, devLink)

	// A new UDP flow to a tunnelled destination, while the server is silent.
	udpConn, err := gonet.DialUDP(dev, nil, &tcpip.FullAddress{
		NIC: 1, Addr: tcpip.AddrFrom4([4]byte{203, 0, 113, 10}), Port: 443,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer udpConn.Close()
	udpConn.Write([]byte("quic initial"))
	time.Sleep(200 * time.Millisecond)

	// The device must still be able to connect anywhere else.
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelDial()
	tcpConn, err := gonet.DialContextTCP(dialCtx, dev, tcpip.FullAddress{
		NIC: 1, Addr: tcpip.AddrFrom4([4]byte{203, 0, 113, 20}), Port: 80,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("packet processing froze behind a UDP flow waiting on a silent server: %v", err)
	}
	tcpConn.Close()

	stopped := make(chan struct{})
	go func() {
		tun.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung with the server silent")
	}
}
