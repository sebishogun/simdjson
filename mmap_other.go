//go:build !unix

package simdjson

// The fallback for platforms without mmap, which is Windows, js/wasm and plan9.
//
// The API is the same so that calling code is not conditional; what it costs is
// the copy the mapping existed to avoid. Said plainly in the doc comment rather
// than hidden, because a caller who chose OpenFile for a four gigabyte document
// needs to know it is four gigabytes of heap here.

import (
	"fmt"
	"os"
)

// A MappedFile is a JSON document read from a file.
//
// On this platform the file is read into memory rather than mapped, so Close
// only drops the reference. Values remain usable after Close, unlike on
// systems with mmap — do not rely on that.
type MappedFile struct {
	data []byte
	doc  *Doc
}

// OpenFile reads path and indexes it.
//
// On systems with mmap this maps the file; here it reads it, so the whole
// document is on the heap. validate chooses between [Parse] and [Scan].
func OpenFile(path string, validate bool) (*MappedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errSyntax("empty file")
	}
	if len(data) > maxDocument {
		return nil, fmt.Errorf("simdjson: %s is %d bytes; documents over %d must be streamed, see NewDecoder and ForEachLineReader",
			path, len(data), maxDocument)
	}
	m := &MappedFile{data: data}
	if validate {
		m.doc, err = Parse(data)
	} else {
		m.doc, err = Scan(data)
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// Doc returns the parsed document.
func (m *MappedFile) Doc() *Doc { return m.doc }

// Bytes returns the file's contents.
func (m *MappedFile) Bytes() []byte { return m.data }

// Close drops the document.
func (m *MappedFile) Close() error {
	m.doc, m.data = nil, nil
	return nil
}
