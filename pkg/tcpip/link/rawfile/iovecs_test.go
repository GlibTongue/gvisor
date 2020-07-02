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

package rawfile

import (
	"testing"
)

func TestIovecBuilderEmpty(t *testing.T) {
	var builder IovecBuilder
	iovecs := builder.Build()
	if got, want := len(iovecs), 0; got != want {
		t.Errorf("len(iovecs) = %d, want %d", got, want)
	}
}

func TestIovecBuilderBuild(t *testing.T) {
	a := []byte{1, 2}
	b := []byte{3, 4, 5}

	var builder IovecBuilder
	builder.Add(a)
	builder.Add(b)
	builder.Add(nil)      // Nil slice won't be added.
	builder.Add([]byte{}) // Empty slice won't be added.
	iovecs := builder.Build()

	if got, want := len(iovecs), 2; got != want {
		t.Fatalf("len(iovecs) = %d, want %d", got, want)
	}
	for i, data := range [][]byte{a, b} {
		if got, want := *iovecs[i].Base, data[0]; got != want {
			t.Fatalf("*iovecs[%d].Base = %d, want %d", i, got, want)
		}
		if got, want := iovecs[i].Len, uint64(len(data)); got != want {
			t.Fatalf("iovecs[%d].Len = %d, want %d", i, got, want)
		}
	}
}
