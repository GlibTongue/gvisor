// Copyright 2019 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at //
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package stack

import (
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/buffer"
)

// NewPacketBufferOptions specifies options for PacketBuffer creation.
type NewPacketBufferOptions struct {
	// ReserveHeaderBytes is the number of bytes to reserve for header portion.
	// Total number of bytes pushed onto the header must not exceed this value.
	ReserveHeaderBytes int

	// Data is the initial unparsed data for the new packet. If set, it will be
	// owned by the new packet.
	Data buffer.VectorisedView
}

// A PacketBuffer contains all the data of a network packet.
//
// As a PacketBuffer traverses up the stack, it may be necessary to pass it to
// multiple endpoints.
//
// The whole packet is expected to be a series of bytes in the following order:
// LinkHeader, NetworkHeader, TransportHeader, and Data. Any of them can be
// empty. Use of PacketBuffer in any other order is unsupported.
type PacketBuffer struct {
	_ noCopy

	// PacketBufferEntry is used to build an intrusive list of
	// PacketBuffers.
	PacketBufferEntry

	// Data holds the payload of the packet.
	//
	// For inbound packets, Data is initially the whole packet. Then gets moved to
	// headers via PacketHeader.Consume, when the packet is being parsed.
	//
	// For outbound packets, Data is the innermost layer, defined by the protocol.
	// Headers are pushed in front of it via PacketHeader.Push.
	//
	// The bytes backing Data are immutable, unless it's being constructed.
	Data buffer.VectorisedView

	// These fields are headers for the packet. See Data for how they are used.
	//
	// Users should not write to its backing storage unless the packet is being
	// constructed.
	LinkHeader      PacketHeader
	NetworkHeader   PacketHeader
	TransportHeader PacketHeader

	// header is the internal storage for outbound packets. Headers will be pushed
	// (prepended) on this storage as the packet is being constructed.
	//
	// TODO(gvisor.dev/issue/2404): Switch to an implementation that header and
	// data are held in the same underlying buffer storage.
	header buffer.Prependable

	// Hash is the transport layer hash of this packet. A value of zero
	// indicates no valid hash has been set.
	Hash uint32

	// Owner is implemented by task to get the uid and gid.
	// Only set for locally generated packets.
	Owner tcpip.PacketOwner

	// The following fields are only set by the qdisc layer when the packet
	// is added to a queue.
	EgressRoute           *Route
	GSOOptions            *GSO
	NetworkProtocolNumber tcpip.NetworkProtocolNumber

	// NatDone indicates if the packet has been manipulated as per NAT
	// iptables rule.
	NatDone bool
}

// NewPacketBuffer creates a new PacketBuffer with opts.
func NewPacketBuffer(opts *NewPacketBufferOptions) *PacketBuffer {
	pk := &PacketBuffer{
		Data: opts.Data,
	}
	pk.LinkHeader.init(pk)
	pk.NetworkHeader.init(pk)
	pk.TransportHeader.init(pk)
	if opts.ReserveHeaderBytes != 0 {
		pk.header = buffer.NewPrependable(opts.ReserveHeaderBytes)
	}
	return pk
}

// ReservedHeaderBytes returns the number of bytes initially reserved for
// header portion.
func (pk *PacketBuffer) ReservedHeaderBytes() int {
	return pk.header.UsedLength() + pk.header.AvailableLength()
}

// Size returns the size of packet in bytes.
func (pk *PacketBuffer) Size() int {
	// Note for inbound packets (Consume called), headers are not stored in
	// pk.header. Thus, calculation of size of each header is needed.
	return pk.LinkHeader.Size() + pk.NetworkHeader.Size() + pk.TransportHeader.Size() + pk.Data.Size()
}

// Views returns the underlying storage of the whole packet.
func (pk *PacketBuffer) Views() []buffer.View {
	// Optimization for outbound packets that headers are in pk.header.
	useHeader := canUseHeader(&pk.LinkHeader) &&
		canUseHeader(&pk.NetworkHeader) &&
		canUseHeader(&pk.TransportHeader)

	data := pk.Data.Views()

	var vs []buffer.View
	if useHeader {
		vs = make([]buffer.View, 0, 1+len(data))
		vs = append(vs, pk.header.View())
	} else {
		vs = make([]buffer.View, 0, 3+len(data))
		if v := pk.LinkHeader.View(); len(v) > 0 {
			vs = append(vs, v)
		}
		if v := pk.NetworkHeader.View(); len(v) > 0 {
			vs = append(vs, v)
		}
		if v := pk.TransportHeader.View(); len(v) > 0 {
			vs = append(vs, v)
		}
	}
	vs = append(vs, data...)
	return vs
}

func canUseHeader(h *PacketHeader) bool {
	// h.offset will be negative if the header was pushed in to prependable
	// portion, or doesn't matter when it's empty.
	return h.Empty() || h.offset < 0
}

// Clone makes a copy of pk. It clones the Data field, which creates a new
// VectorisedView but does not deep copy the underlying bytes.
//
// Clone also does not deep copy any of its other fields.
//
// FIXME(b/153685824): Data gets copied but not other header references.
func (pk *PacketBuffer) Clone() *PacketBuffer {
	newPk := &PacketBuffer{
		PacketBufferEntry:     pk.PacketBufferEntry,
		Data:                  pk.Data.Clone(nil),
		header:                pk.header.DeepCopy(),
		Hash:                  pk.Hash,
		Owner:                 pk.Owner,
		EgressRoute:           pk.EgressRoute,
		GSOOptions:            pk.GSOOptions,
		NetworkProtocolNumber: pk.NetworkProtocolNumber,
		NatDone:               pk.NatDone,
	}
	newPk.LinkHeader = pk.LinkHeader.clone(newPk)
	newPk.NetworkHeader = pk.NetworkHeader.clone(newPk)
	newPk.TransportHeader = pk.TransportHeader.clone(newPk)
	return newPk
}

// PacketHeader is the logical view into a header of underlying packet.
type PacketHeader struct {
	pk     *PacketBuffer
	buf    buffer.View
	offset int
}

// init is called internally by PacketBuffer.
func (h *PacketHeader) init(pk *PacketBuffer) {
	h.pk = pk
}

// clone is called internally by PacketBuffer.
func (h *PacketHeader) clone(newPk *PacketBuffer) PacketHeader {
	var newBuf buffer.View
	if h.offset < 0 {
		// In header.
		l := len(h.buf)
		v := newPk.header.View()
		newBuf = v[len(v)+h.offset:][:l:l]
	} else {
		newBuf = append(newBuf, h.buf...)
	}
	return PacketHeader{
		pk:     newPk,
		buf:    newBuf,
		offset: h.offset,
	}
}

// Size returns the size of h in bytes.
func (h *PacketHeader) Size() int {
	return len(h.buf)
}

// Empty returns whether h is empty or zero-length.
func (h *PacketHeader) Empty() bool {
	return len(h.buf) == 0
}

// View returns the underlying storage of h.
func (h *PacketHeader) View() buffer.View {
	return h.buf
}

// AvailableLength returns number of bytes available in the reserved header
// space. This is relevant to Push method only.
func (h *PacketHeader) AvailableLength() int {
	return h.pk.header.AvailableLength()
}

// Push pushes size bytes in the front of its residing packet. Push and Consume
// must only be called at most once in total.
func (h *PacketHeader) Push(size int) buffer.View {
	if h.buf != nil {
		panic("Push must not be called twice")
	}
	h.buf = buffer.View(h.pk.header.Prepend(size))
	h.offset = -h.pk.header.UsedLength()
	return h.buf
}

// Consume move the first size bytes of the unparsed data portion in the packet
// to h. Push and Consume must only be called at most once in total.
func (h *PacketHeader) Consume(size int) (buffer.View, bool) {
	if h.buf != nil {
		panic("Consume must not be called twice")
	}
	v, ok := h.pk.Data.PullUp(size)
	if !ok {
		return nil, false
	}
	h.pk.Data.TrimFront(size)
	h.buf = v
	return h.buf, true
}

// PayloadSince returns packet payload since a particular header. This method
// isn't optimized and should be used in test only.
func PayloadSince(h *PacketHeader) buffer.View {
	var v buffer.View
	switch h {
	case &h.pk.LinkHeader:
		v = append(v, h.pk.LinkHeader.View()...)
		fallthrough
	case &h.pk.NetworkHeader:
		v = append(v, h.pk.NetworkHeader.View()...)
		fallthrough
	case &h.pk.TransportHeader:
		v = append(v, h.pk.TransportHeader.View()...)

	default:
		panic("header does not belong to PacketBuffer anymore")
	}

	v = append(v, h.pk.Data.ToView()...)
	return v
}

// noCopy may be embedded into structs which must not be copied
// after the first use.
//
// See https://golang.org/issues/8005#issuecomment-190753527
// for details.
type noCopy struct{}

// Lock is a no-op used by -copylocks checker from `go vet`.
func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
