//go:build unix

package simdjson

// Mapping a file instead of reading it.
//
// No JSON library in Go does this, which the API survey confirmed. It matters
// for exactly the case this package is otherwise weakest at: a file too big to
// want a copy of. Reading a four gigabyte document means four gigabytes of heap
// before the parse starts and a second four while the index is built; mapping
// it means the pages arrive as they are touched and leave under memory pressure
// without the collector being involved.
//
// The index is still built over the whole file, which is 0.93x its size, so
// this is not free. What it removes is the copy, the allocation, and the
// collector's view of the document as a live object.

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// A MappedFile is a JSON document mapped into memory.
//
// Close must be called, and no [Value] taken from the document may be used
// afterwards: the bytes go away with the mapping, and reading them then is a
// segmentation fault rather than a Go panic. [Value.String] copies, so a string
// taken from it outlives Close; [Value.StringNoCopy] and [Value.Raw] do not.
type MappedFile struct {
	data []byte
	f    *os.File
	doc  *Doc
}

// OpenFile maps path into memory and indexes it.
//
// validate says whether to prove the whole document well-formed, which is the
// difference between [Parse] and [Scan]: validating a two gigabyte file costs
// about four times what indexing it does, and a caller pulling one field out of
// a log does not need it.
func OpenFile(path string, validate bool) (*MappedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	size := st.Size()
	if size == 0 {
		f.Close()
		return nil, errSyntax("empty file")
	}
	if size > maxDocument {
		f.Close()
		return nil, fmt.Errorf("simdjson: %s is %d bytes; documents over %d must be streamed, see NewDecoder and ForEachLineReader",
			path, size, maxDocument)
	}
	data, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, err
	}
	// Sequential, because a parse walks the document front to back several
	// times and the kernel reading ahead is most of what makes this quick. An
	// advisory call that fails is not a reason to fail the open.
	_ = unix.Madvise(data, unix.MADV_SEQUENTIAL)

	m := &MappedFile{data: data, f: f}
	if validate {
		m.doc, err = Parse(data)
	} else {
		m.doc, err = Scan(data)
	}
	if err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// Doc returns the parsed document. It is valid until [MappedFile.Close].
func (m *MappedFile) Doc() *Doc { return m.doc }

// Bytes returns the mapped file's contents. It is valid until
// [MappedFile.Close], and writing to it will fault: the mapping is read-only.
func (m *MappedFile) Bytes() []byte { return m.data }

// Close unmaps the file and closes it.
func (m *MappedFile) Close() error {
	m.doc = nil
	var err error
	if m.data != nil {
		err = unix.Munmap(m.data)
		m.data = nil
	}
	if m.f != nil {
		if cerr := m.f.Close(); err == nil {
			err = cerr
		}
		m.f = nil
	}
	return err
}
