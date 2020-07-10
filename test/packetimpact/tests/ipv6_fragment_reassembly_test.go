// Copyright 2020 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ipv6_fragment_reassembly_test

import (
	"encoding/binary"
	"flag"
	"net"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/test/packetimpact/testbench"
	tb "gvisor.dev/gvisor/test/packetimpact/testbench"
)

const (
	payloadLength = 752
)

func init() {
	testbench.RegisterFlags(flag.CommandLine)
}

func TestIPv6FragmentReassembly(t *testing.T) {
	dut := tb.NewDUT(t)
	defer dut.TearDown()
	conn := tb.NewIPv6Conn(t, tb.IPv6{}, tb.IPv6{})
	defer conn.Close()

	data := make([]byte, payloadLength)
	for i := range data {
		data[i] = 'A'
	}

	icmpv6EchoPayload := make([]byte, 4)
	binary.BigEndian.PutUint16(icmpv6EchoPayload[0:2], 0)
	binary.BigEndian.PutUint16(icmpv6EchoPayload[2:4], 0)
	icmpv6EchoPayload = append(icmpv6EchoPayload, data...)

	lIP := tcpip.Address(net.ParseIP(tb.LocalIPv6).To16())
	rIP := tcpip.Address(net.ParseIP(tb.RemoteIPv6).To16())
	icmpv6 := tb.ICMPv6{
		Type:    tb.ICMPv6Type(header.ICMPv6EchoRequest),
		Code:    tb.Byte(0),
		Payload: icmpv6EchoPayload,
	}
	icmpv6Bytes, err := icmpv6.ToBytes()
	if err != nil {
		t.Fatalf("failed to serialize ICMPv6: %s")
	}
	cksum := header.ICMPv6Checksum(header.ICMPv6(icmpv6Bytes), lIP, rIP, buffer.NewVectorisedView(len(data), []buffer.View{data}))

	conn.Send(tb.IPv6{},
		&tb.IPv6FragmentExtHdr{
			FragmentOffset: tb.Uint16(0),
			MoreFragments:  tb.Bool(true),
			Identification: tb.Uint32(42),
		},
		&tb.ICMPv6{
			Type:     tb.ICMPv6Type(header.ICMPv6EchoRequest),
			Code:     tb.Byte(0),
			Payload:  icmpv6EchoPayload,
			Checksum: &cksum,
		})

	icmpv6ProtoNum := header.IPv6ExtensionHeaderIdentifier(header.ICMPv6ProtocolNumber)

	conn.Send(tb.IPv6{},
		&tb.IPv6FragmentExtHdr{
			NextHeader:     &icmpv6ProtoNum,
			FragmentOffset: tb.Uint16((payloadLength + header.ICMPv6EchoMinimumSize) / 8),
			MoreFragments:  tb.Bool(false),
			Identification: tb.Uint32(42),
		},
		&tb.Payload{
			Bytes: data,
		})

	gotEchoReplyFirstPart, err := conn.ExpectFrame(tb.Layers{
		&tb.Ether{},
		&tb.IPv6{},
		&tb.IPv6FragmentExtHdr{
			FragmentOffset: tb.Uint16(0),
			MoreFragments:  tb.Bool(true),
		},
		&tb.ICMPv6{
			Type: tb.ICMPv6Type(header.ICMPv6EchoReply),
			Code: tb.Byte(0),
		},
	}, time.Second)
	if err != nil {
		t.Fatalf("expected a fragmented ICMPv6 Echo Reply, but got none: %s", err)
	}

	payload, err := gotEchoReplyFirstPart[len(gotEchoReplyFirstPart)-1].ToBytes()
	if err != nil {
		t.Fatalf("failed to serialize ICMPv6: %s", err)
	}
	receivedLen := len(payload)
	expectedLen := payloadLength*2 - (receivedLen - header.ICMPv6EchoMinimumSize)
	for _, b := range payload[header.ICMPv6EchoMinimumSize:] {
		if b != 'A' {
			t.Fatalf("expected all A's in the payload")
		}
	}

	// NOTE: Since the current parser is stateless, we will recognize
	// the payload as an ICMPv6 packet because of the next header
	// value in the fragment header, but in fact it will not contain
	// an ICMPv6 header as it is already included in the first
	// fragment. A possible solution is to let the ipv6State track
	// fragmentation and make parseXXX functions consult the state.
	// What we are currently doing here is a bit hacky: we manually
	// construct a fake ICMPv6 layer which, after serialization, has
	// the bytes we wanted.
	fakeType := header.ICMPv6Type('A')
	fakeCode := byte('A')
	fakeCksum := uint16(0x4141)
	fakePayload := data[:expectedLen-4]
	if _, err := conn.ExpectFrame(tb.Layers{
		&tb.Ether{},
		&tb.IPv6{},
		&tb.IPv6FragmentExtHdr{
			NextHeader:     &icmpv6ProtoNum,
			FragmentOffset: tb.Uint16(uint16(receivedLen / 8)),
			MoreFragments:  tb.Bool(false),
		},
		&tb.ICMPv6{
			Type:     &fakeType,
			Code:     &fakeCode,
			Checksum: &fakeCksum,
			Payload:  fakePayload,
		},
	}, time.Second); err != nil {
		t.Fatalf("expected the rest of ICMPv6 Echo Reply, but got none: %s", err)
	}
}
