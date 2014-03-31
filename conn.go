// Copyright 2014 Garrett D'Amore
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use file except in compliance with the License.
// You may obtain a copy of the license at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sp

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
)

// conn implements the Pipe interface on top of net.Conn.  The
// assumption is that transports using this have similar wire protocols,
// and conn is meant to be used as a building block.
//
type conn struct {
	conn   net.Conn
	rlock  sync.Mutex
	wlock  sync.Mutex
	rproto uint16
	lproto uint16
	open   bool
}

// Recv implements the Pipe Recv method.  The message received is expected as
// a 64-bit size (network byte order) followed by the message itself.
func (p *conn) Recv() (*Message, error) {

	var sz int64
	var err error
	var msg *Message

	// prevent interleaved reads
	p.rlock.Lock()
	defer p.rlock.Unlock()

	if err = binary.Read(p.conn, binary.BigEndian, &sz); err != nil {
		return nil, err
	}

	// TBD: This fixed limit is kind of silly, but it keeps
	// a bogus peer from causing us to try to allocate ridiculous
	// amounts of memory.  If you don't like it, then prealloc
	// a buffer.  But for protocols that only use small messages
	// this can actually be more efficient since we don't allocate
	// any more space than our peer says we need to.
	if sz > 1024*1024 || sz < 0 {
		p.conn.Close()
		return nil, ErrTooLong
	}
	msg = &Message{Body: make([]byte, sz)}
	if _, err = io.ReadFull(p.conn, msg.Body); err != nil {
		return nil, err
	}
	return msg, nil
}

// Send implements the Pipe Send method.  The message is sent as a 64-bit
// size (network byte order) followed by the message itself.
func (p *conn) Send(msg *Message) error {

	h := make([]byte, 8)
	l := uint64(len(msg.Header) + len(msg.Body))
	putUint64(h, l)

	// prevent interleaved writes
	p.wlock.Lock()
	defer p.wlock.Unlock()

	// send length header
	if err := binary.Write(p.conn, binary.BigEndian, l); err != nil {
		return err
	}
	if _, err := p.conn.Write(msg.Header); err != nil {
		return err
	}
	// hope this works
	if _, err := p.conn.Write(msg.Body); err != nil {
		return err
	}
	return nil
}

// LocalProtocol returns our local protocol number.
func (p *conn) LocalProtocol() uint16 {
	return p.lproto
}

// RemoteProtocol returns our peer's protocol number.
func (p *conn) RemoteProtocol() uint16 {
	return p.rproto
}

// Close implements the Pipe Close method.
func (p *conn) Close() error {
	p.open = false
	return p.conn.Close()
}

// IsOpen implements the PipeIsOpen method.
func (p *conn) IsOpen() bool {
	return p.open
}

// NewConnPipe allocates a new Pipe using the supplied net.Conn, and
// initializes it.  It performs the handshake required at the SP layer,
// only returning the Pipe once the SP layer negotiation is complete.
//
// Stream oriented transports can utilize this to implement a Transport.
// The implementation will also need to implement PipeDialer, PipeAccepter,
// and the Transport enclosing structure.   Using this layered interface,
// the implementation needn't bother concerning itself with passing actual
// SP messages once the lower layer connection is established.
func NewConnPipe(c net.Conn, lproto uint16) (Pipe, error) {
	p := &conn{conn: c, lproto: lproto}
	if err := p.handshake(); err != nil {
		return nil, err
	}

	return p, nil
}

// connHeader is exchanged during the initial handshake.
type connHeader struct {
	Zero    byte // must be zero
	S       byte // 'S'
	P       byte // 'P'
	Version byte // only zero at present
	Proto   uint16
	Rsvd    uint16 // always zero at present
}

// handshake establishes an SP connection between peers.  Both sides must
// send the header, then both sides must wait for the peer's header.
// As a side effect, the peer's protocol number is stored in the conn.
func (p *conn) handshake() error {
	var err error

	h := connHeader{S: 'S', P: 'P', Proto: p.lproto}
	if err = binary.Write(p.conn, binary.BigEndian, &h); err != nil {
		return err
	}
	if err = binary.Read(p.conn, binary.BigEndian, &h); err != nil {
		p.conn.Close()
		return err
	}
	if h.Zero != 0 || h.S != 'S' || h.P != 'P' || h.Rsvd != 0 {
		p.conn.Close()
		return ErrBadHeader
	}
	// The only version number we support at present is "0", at offset 3.
	if h.Version != 0 {
		p.conn.Close()
		return ErrBadVersion
	}

	// The protocol number lives as 16-bits (big-endian) at offset 4.
	p.rproto = h.Proto
	p.open = true
	return nil
}