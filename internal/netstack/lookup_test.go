package netstack

import (
	"context"
	"io"
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// dnsPipe answers the one query written to it with the given addresses.
type dnsPipe struct {
	answer []net.IP
	out    chan []byte
}

func (d *dnsPipe) Write(q []byte) (int, error) {
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		return 0, err
	}
	question, err := p.Question()
	if err != nil {
		return 0, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true})
	b.StartQuestions()
	b.Question(question)
	b.StartAnswers()
	rh := dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET, TTL: 60}
	for _, ip := range d.answer {
		if v4 := ip.To4(); v4 != nil && question.Type == dnsmessage.TypeA {
			var a [4]byte
			copy(a[:], v4)
			b.AResource(rh, dnsmessage.AResource{A: a})
		}
	}
	msg, _ := b.Finish()
	d.out <- msg
	return len(q), nil
}

func (d *dnsPipe) Read(p []byte) (int, error) {
	msg, ok := <-d.out
	if !ok {
		return 0, io.EOF
	}
	return copy(p, msg), nil
}

func (d *dnsPipe) Close() error { return nil }

func TestExchangeParsesTheAnswer(t *testing.T) {
	pipe := &dnsPipe{answer: []net.IP{net.ParseIP("162.159.137.232"), net.ParseIP("162.159.138.232")}, out: make(chan []byte, 1)}
	name := dnsmessage.MustNewName("discord.com.")
	ips, err := exchange(context.Background(), pipe, name, dnsmessage.TypeA, 7)
	if err != nil || len(ips) != 2 || !ips[0].Equal(net.ParseIP("162.159.137.232")) {
		t.Fatalf("exchange = %v, %v", ips, err)
	}
}
