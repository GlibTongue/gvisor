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

// +build linux

// Package iovec provides helpers to interact with vectorized I/O on host
// system.
package iovec

import (
	"syscall"

	"gvisor.dev/gvisor/pkg/abi/linux"
)

// MaxIovs is the maximum number of iovecs host platform can accept.
var MaxIovs = linux.UIO_MAXIOV

// Builder is a builder for slice of syscall.Iovec.
type Builder struct {
	iovec   []syscall.Iovec
	storage [8]syscall.Iovec

	// overflow tracks the last buffer when iovec length is at MaxIovs.
	overflow []byte
}

// Add adds buf to w preparing to be written. Zero-length buf won't be added.
func (w *Builder) Add(buf []byte) {
	if len(buf) == 0 {
		return
	}
	if w.iovec == nil {
		w.iovec = w.storage[:0]
	}
	if len(w.iovec) >= MaxIovs {
		w.addByAppend(buf)
		return
	}
	w.iovec = append(w.iovec, syscall.Iovec{
		Base: &buf[0],
		Len:  uint64(len(buf)),
	})
	// Keep the last buf if iovec is at max capacity. We will need to append to it
	// for later bufs.
	if len(w.iovec) == MaxIovs {
		n := len(buf)
		w.overflow = buf[:n:n]
	}
}

func (w *Builder) addByAppend(buf []byte) {
	w.overflow = append(w.overflow, buf...)
	w.iovec[len(w.iovec)-1] = syscall.Iovec{
		Base: &w.overflow[0],
		Len:  uint64(len(w.overflow)),
	}
}

// Build returns the final Iovec slice. The length of returned iovec will not
// excceed MaxIovs.
func (w *Builder) Build() []syscall.Iovec {
	return w.iovec
}
