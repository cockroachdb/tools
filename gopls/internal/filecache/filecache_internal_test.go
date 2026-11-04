// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filecache

// This file tests the file-system behavior of Set and get,
// bypassing the in-memory cache that Get consults first.

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func randomBytes(t testing.TB, n int) []byte {
	data := make([]byte, n)
	if _, err := cryptorand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return data
}

// TestSetRecreatesDeletedDir checks that Set recreates the "XX/"
// directories of its files if the garbage collector (in this or
// another process) has deleted them.
func TestSetRecreatesDeletedDir(t *testing.T) {
	const kind = "TestSetRecreatesDeletedDir"
	var key [32]byte
	copy(key[:], randomBytes(t, len(key)))
	value := randomBytes(t, 100)

	if err := Set(kind, key, value); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		mustFilename(t, kind, key),
		mustFilename(t, casKind, sha256.Sum256(value)),
	} {
		if err := os.RemoveAll(filepath.Dir(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := Set(kind, key, value); err != nil {
		t.Fatalf("Set after directory deletion failed: %v", err)
	}
	if got, err := get(kind, key); err != nil || !bytes.Equal(got, value) {
		t.Fatalf("get after Set = %q, %v; want %q", got, err, value)
	}
}

// TestSetRepairsCorruptCAS checks that Set does not leave in place a
// CAS file that is truncated or has the wrong contents (for example,
// because a previous Set was killed or the disk filled up).
func TestSetRepairsCorruptCAS(t *testing.T) {
	const kind = "TestSetRepairsCorruptCAS"
	value := randomBytes(t, 4096)
	casName := mustFilename(t, casKind, sha256.Sum256(value))

	for _, corrupt := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"truncated", value[:100]},
		{"wrong contents, right size", randomBytes(t, len(value))},
	} {
		var key [32]byte
		copy(key[:], randomBytes(t, len(key)))

		if err := os.MkdirAll(filepath.Dir(casName), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(casName, corrupt.data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := get(kind, key); err != ErrNotFound {
			t.Fatalf("%s: get before Set = %v, want ErrNotFound", corrupt.name, err)
		}
		if err := Set(kind, key, value); err != nil {
			t.Fatalf("%s: Set failed: %v", corrupt.name, err)
		}
		if got, err := get(kind, key); err != nil || !bytes.Equal(got, value) {
			t.Fatalf("%s: get after Set = %q, %v; want the value", corrupt.name, got, err)
		}
	}
}

func mustFilename(t testing.TB, kind string, key [32]byte) string {
	name, err := filename(kind, key)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// BenchmarkGetFile measures the cost of reading an entry from the
// file system (two files), as opposed to the in-memory cache.
func BenchmarkGetFile(b *testing.B) {
	const kind = "BenchmarkGetFile"
	var key [32]byte
	copy(key[:], randomBytes(b, len(key)))
	value := randomBytes(b, 8192)
	if err := Set(kind, key, value); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(value)))
	for b.Loop() {
		if _, err := get(kind, key); err != nil {
			b.Fatal(err)
		}
	}
}
