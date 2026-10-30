// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/tools/gopls/internal/file"
	"golang.org/x/tools/gopls/internal/protocol"
)

type fakeHandle struct {
	uri     protocol.DocumentURI
	content []byte
}

func (h fakeHandle) URI() protocol.DocumentURI { return h.uri }
func (h fakeHandle) Identity() file.Identity {
	return file.Identity{URI: h.uri, Hash: file.HashOf(h.content)}
}
func (h fakeHandle) SameContentsOnDisk() bool    { return true }
func (h fakeHandle) Version() int32              { return 0 }
func (h fakeHandle) Content() ([]byte, error)    { return h.content, nil }
func (h fakeHandle) ModTime() (time.Time, error) { return time.Time{}, nil }
func (h fakeHandle) String() string              { return h.uri.Path() }

func TestRootMapper(t *testing.T) {
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")
	mA := newRootMapper(protocol.URIFromPath(dirA))
	mB := newRootMapper(protocol.URIFromPath(dirB))

	nameA := filepath.Join(dirA, "p", "p.go")
	nameB := filepath.Join(dirB, "p", "p.go")
	uriA := protocol.URIFromPath(nameA)
	uriB := protocol.URIFromPath(nameB)
	outside := filepath.Join(filepath.Dir(dirA), "a2", "p.go") // shares a string prefix with dirA

	// Keys of the same file, relative to each root, are equal,
	// unless the contents differ.
	content := []byte("package p")
	if kA, kB := mA.fileKey(fakeHandle{uriA, content}), mB.fileKey(fakeHandle{uriB, content}); kA != kB {
		t.Errorf("fileKey differs across roots: %q vs %q", kA, kB)
	}
	if kA, kB := mA.fileKey(fakeHandle{uriA, content}), mB.fileKey(fakeHandle{uriB, []byte("package q")}); kA == kB {
		t.Errorf("fileKey equal for different contents: %q", kA)
	}
	// Files outside the root keep their absolute identity.
	outURI := protocol.URIFromPath(outside)
	if got, want := mA.fileKey(fakeHandle{outURI, content}), (fakeHandle{outURI, content}).Identity().String(); got != want {
		t.Errorf("fileKey(outside) = %q, want %q", got, want)
	}

	// Encoding under one root and decoding under the other rebases names.
	if got := mB.decPath(mA.encPath(nameA)); got != nameB {
		t.Errorf("decPath(encPath(%q)) = %q, want %q", nameA, got, nameB)
	}
	if got := mB.decURI(mA.encURI(uriA)); got != uriB {
		t.Errorf("decURI(encURI(%q)) = %q, want %q", uriA, got, uriB)
	}
	if got := mA.encPath(outside); got != outside {
		t.Errorf("encPath(%q) = %q, want unchanged", outside, got)
	}
	if got := mA.encPath(dirA); got != dirA {
		t.Errorf("encPath(root) = %q, want unchanged", got)
	}

	// Diagnostics are rebased, including related information, edits, and messages.
	diag := gobDiagnostic{
		Location: protocol.Location{URI: uriA},
		Message:  "see " + nameA,
		Related:  []gobRelatedInformation{{Location: protocol.Location{URI: uriA}, Message: "other declaration"}},
		SuggestedFixes: []gobSuggestedFix{{
			TextEdits: []gobTextEdit{{Location: protocol.Location{URI: uriA}}},
			Command:   &gobCommand{Arguments: []json.RawMessage{json.RawMessage(`{"URI":"` + string(uriA) + `"}`)}},
		}},
	}
	enc := mA.encDiagnostic(diag)
	if enc.Location.URI == uriA || enc.Related[0].Location.URI == uriA || diag.Location.URI != uriA {
		t.Fatalf("encDiagnostic did not encode a copy: %+v", enc)
	}
	dec := mB.decDiagnostic(enc)
	if dec.Location.URI != uriB ||
		dec.Message != "see "+nameB ||
		dec.Related[0].Location.URI != uriB ||
		dec.SuggestedFixes[0].TextEdits[0].Location.URI != uriB ||
		string(dec.SuggestedFixes[0].Command.Arguments[0]) != `{"URI":"`+string(uriB)+`"}` {
		t.Errorf("decDiagnostic(encDiagnostic(d)) = %+v, want names under %s", dec, dirB)
	}

	// The zero mapper is the identity.
	var zero rootMapper
	if got := zero.encPath(nameA); got != nameA {
		t.Errorf("zero.encPath(%q) = %q", nameA, got)
	}
}
