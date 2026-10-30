// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

// This file defines rootMapper, which makes file-cache keys and values
// independent of the absolute location of the workspace.
//
// Two checkouts of the same source tree (for example, two git worktrees
// of one repository at the same commit) produce the same type-checking
// results, apart from the location of the checkout. Without this
// mapping, the file-cache key of each workspace package depends on the
// absolute name of each of its files, and so a new checkout cannot reuse
// anything that gopls computed for another checkout.
//
// A rootMapper replaces the view root prefix of each file name or URI
// with a sentinel, both in keys and in cached values, and does the
// reverse when it reads a value back. Files outside the root (GOROOT,
// GOMODCACHE, GOCACHE, ...) keep their absolute names, as their
// locations are already stable.
//
// Invariant: if two packages in views with different roots have the
// same key, then their cached values are equal after the root of each
// is replaced by the sentinel. Every value stored under a key derived
// from rootMapper.fileKey must therefore be encoded with encPath,
// encURI, or encText, and decoded with the inverse function of the
// same mapper.
//
// Decoded values held by the filecache memory cache are shared by all
// views of the process, so they must stay in the sentinel form; the
// decoding to absolute names is done after each cache lookup.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/tools/gopls/internal/file"
	"golang.org/x/tools/gopls/internal/protocol"
)

// The sentinels contain a NUL byte, which cannot occur in a file name.
const (
	rootPathSentinel = "\x00gopls-root-path\x00"
	rootURISentinel  = "\x00gopls-root-uri\x00"
)

// A rootMapper maps file names and URIs under a root directory to and
// from a location-independent form. The zero value maps nothing.
type rootMapper struct {
	dir string               // root directory, without trailing separator; "" => disabled
	uri protocol.DocumentURI // URI of dir, without trailing slash
}

// newRootMapper returns a rootMapper for the given root directory.
func newRootMapper(root protocol.DocumentURI) rootMapper {
	if root == "" {
		return rootMapper{}
	}
	dir := strings.TrimSuffix(filepath.Clean(root.Path()), string(filepath.Separator))
	uri := protocol.DocumentURI(strings.TrimSuffix(string(protocol.URIFromPath(dir)), "/"))
	if dir == "" || dir == "." || uri == "" {
		return rootMapper{} // e.g. root is "/"
	}
	return rootMapper{dir: dir, uri: uri}
}

// rootMapper returns the rootMapper for the snapshot's view.
func (s *Snapshot) rootMapper() rootMapper {
	return newRootMapper(s.view.root)
}

// fileKey returns the location-independent identity of a file, for use
// in cache keys: its name relative to the root (if it is under the
// root) and its content hash.
func (m rootMapper) fileKey(fh file.Handle) string {
	id := fh.Identity()
	return fmt.Sprintf("%s%s", m.encURI(id.URI), id.Hash)
}

func (m rootMapper) encPath(name string) string {
	if m.dir != "" {
		if rest, ok := strings.CutPrefix(name, m.dir); ok && strings.HasPrefix(rest, string(filepath.Separator)) {
			return rootPathSentinel + rest
		}
	}
	return name
}

func (m rootMapper) decPath(name string) string {
	if rest, ok := strings.CutPrefix(name, rootPathSentinel); ok {
		return m.dir + rest
	}
	return name
}

func (m rootMapper) encURI(uri protocol.DocumentURI) protocol.DocumentURI {
	if m.uri != "" {
		if rest, ok := strings.CutPrefix(string(uri), string(m.uri)); ok && strings.HasPrefix(rest, "/") {
			return protocol.DocumentURI(rootURISentinel + rest)
		}
	}
	return uri
}

func (m rootMapper) decURI(uri protocol.DocumentURI) protocol.DocumentURI {
	if rest, ok := strings.CutPrefix(string(uri), rootURISentinel); ok {
		return m.uri + protocol.DocumentURI(rest)
	}
	return uri
}

// encText replaces each occurrence of the root, in file name or URI
// form, within free text such as a diagnostic message.
func (m rootMapper) encText(s string) string {
	if m.dir == "" || !strings.Contains(s, m.dir) {
		return s // fast path (the URI form contains dir, modulo escaping)
	}
	s = strings.ReplaceAll(s, string(m.uri)+"/", rootURISentinel+"/")
	s = strings.ReplaceAll(s, m.dir+string(filepath.Separator), rootPathSentinel+string(filepath.Separator))
	return s
}

func (m rootMapper) decText(s string) string {
	if !strings.Contains(s, "\x00") {
		return s // fast path
	}
	s = strings.ReplaceAll(s, rootURISentinel, string(m.uri))
	s = strings.ReplaceAll(s, rootPathSentinel, m.dir)
	return s
}

// rename returns a copy of the diagnostic in which each URI and each
// message is mapped by uri and text, respectively.
func (d gobDiagnostic) rename(uri func(protocol.DocumentURI) protocol.DocumentURI, text func(string) string) gobDiagnostic {
	d.Location.URI = uri(d.Location.URI)
	d.Message = text(d.Message)
	if d.Related != nil {
		related := make([]gobRelatedInformation, len(d.Related))
		for i, r := range d.Related {
			r.Location.URI = uri(r.Location.URI)
			r.Message = text(r.Message)
			related[i] = r
		}
		d.Related = related
	}
	if d.SuggestedFixes != nil {
		fixes := make([]gobSuggestedFix, len(d.SuggestedFixes))
		for i, fix := range d.SuggestedFixes {
			if fix.TextEdits != nil {
				edits := make([]gobTextEdit, len(fix.TextEdits))
				for j, e := range fix.TextEdits {
					e.Location.URI = uri(e.Location.URI)
					edits[j] = e
				}
				fix.TextEdits = edits
			}
			if cmd := fix.Command; cmd != nil {
				// Arguments are JSON, which may contain URIs or file names.
				// The sentinel form is never parsed, so it need not be valid JSON.
				cmd2 := *cmd
				cmd2.Arguments = make([]json.RawMessage, len(cmd.Arguments))
				for j, arg := range cmd.Arguments {
					if s := text(string(arg)); s != string(arg) {
						arg = json.RawMessage(s)
					} else {
						arg = bytes.Clone(arg)
					}
					cmd2.Arguments[j] = arg
				}
				fix.Command = &cmd2
			}
			fixes[i] = fix
		}
		d.SuggestedFixes = fixes
	}
	return d
}

func (m rootMapper) encDiagnostic(d gobDiagnostic) gobDiagnostic {
	if m.dir == "" {
		return d
	}
	return d.rename(m.encURI, m.encText)
}

func (m rootMapper) decDiagnostic(d gobDiagnostic) gobDiagnostic {
	if m.dir == "" {
		return d
	}
	return d.rename(m.decURI, m.decText)
}
