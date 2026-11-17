// Package filecrypt encrypts stored share files with chunked AES-256-GCM.
//
// Design goals: cheap (one streaming pass, hardware AES), seekable (HTTP Range
// and media preview keep working) and tamper/truncation evident.
//
// File layout:
//
//	magic "SNDF" | version (1) | salt (32) | chunk 0 | chunk 1 | ...
//
// Every chunk holds up to ChunkSize plaintext bytes followed by a 16 byte GCM
// tag. Only the last chunk may be shorter; an empty file is one empty chunk.
// Each file has its own key: HKDF-SHA256(master, salt). The nonce is the chunk
// index and the additional data is a one byte "last chunk" flag, so chunks
// cannot be reordered, dropped or appended without failing authentication.
package filecrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
)

const (
	ChunkSize  = 64 << 10
	tagSize    = 16
	saltSize   = 32
	headerSize = 4 + 1 + saltSize
	version    = 1
)

var (
	magic = [4]byte{'S', 'N', 'D', 'F'}

	ErrCorrupt = errors.New("encrypted file is corrupt or has been modified")
)

// MasterKey derives the file-encryption master key from the 32 byte database key.
func MasterKey(dbKey []byte) ([]byte, error) {
	if len(dbKey) != 32 {
		return nil, errors.New("database key must contain 32 bytes")
	}
	return hkdf.Key(sha256.New, dbKey, nil, "sendora share file encryption v1", 32)
}

func fileAEAD(master, salt []byte) (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, master, salt, "sendora share file key", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(aead cipher.AEAD, idx uint64) []byte {
	n := make([]byte, aead.NonceSize())
	binary.BigEndian.PutUint64(n[len(n)-8:], idx)
	return n
}

func flag(last bool) []byte {
	if last {
		return []byte{1}
	}
	return []byte{0}
}

// CipherSize is the stored size for plain bytes of plaintext.
func CipherSize(plain int64) int64 {
	chunks := plain / ChunkSize
	if plain%ChunkSize != 0 || plain == 0 {
		chunks++
	}
	return headerSize + plain + chunks*tagSize
}

// Encrypt streams src into dst. It returns the plaintext length and, when h is
// not nil, writes every plaintext byte to h so callers get a checksum in the
// same pass.
func Encrypt(dst io.Writer, src io.Reader, master []byte, h hash.Hash) (int64, error) {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return 0, err
	}
	aead, err := fileAEAD(master, salt)
	if err != nil {
		return 0, err
	}
	hdr := make([]byte, 0, headerSize)
	hdr = append(hdr, magic[:]...)
	hdr = append(hdr, version)
	hdr = append(hdr, salt...)
	if _, err := dst.Write(hdr); err != nil {
		return 0, err
	}
	cur := make([]byte, ChunkSize)
	next := make([]byte, ChunkSize)
	out := make([]byte, 0, ChunkSize+tagSize)
	n, err := io.ReadFull(src, cur)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, err
	}
	var total int64
	for idx := uint64(0); ; idx++ {
		last := n < ChunkSize
		nn := 0
		if !last {
			nn, err = io.ReadFull(src, next)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return total, err
			}
			last = nn == 0
		}
		if h != nil {
			h.Write(cur[:n])
		}
		out = aead.Seal(out[:0], nonce(aead, idx), cur[:n], flag(last))
		if _, err := dst.Write(out); err != nil {
			return total, err
		}
		total += int64(n)
		if last {
			return total, nil
		}
		cur, next = next, cur
		n = nn
	}
}

// File is the random-access source Reader needs (an *os.File).
type File interface {
	io.ReaderAt
	io.Closer
}

// Reader is a seekable plaintext view of an encrypted file.
type Reader struct {
	f      File
	aead   cipher.AEAD
	plain  int64
	chunks int64
	pos    int64
	idx    int64 // cached chunk index, -1 when empty
	buf    []byte
	raw    []byte
}

// NewReader validates the header and the stored length against plain, the
// plaintext size recorded in the database.
func NewReader(f File, storedSize, plain int64, master []byte) (*Reader, error) {
	if plain < 0 || storedSize != CipherSize(plain) {
		return nil, ErrCorrupt
	}
	hdr := make([]byte, headerSize)
	if _, err := f.ReadAt(hdr, 0); err != nil {
		return nil, ErrCorrupt
	}
	if [4]byte(hdr[:4]) != magic || hdr[4] != version {
		return nil, ErrCorrupt
	}
	aead, err := fileAEAD(master, hdr[5:])
	if err != nil {
		return nil, err
	}
	chunks := plain / ChunkSize
	if plain%ChunkSize != 0 || plain == 0 {
		chunks++
	}
	return &Reader{f: f, aead: aead, plain: plain, chunks: chunks, idx: -1,
		raw: make([]byte, ChunkSize+tagSize)}, nil
}

func (r *Reader) load(idx int64) error {
	if r.idx == idx {
		return nil
	}
	size := int64(ChunkSize)
	last := idx == r.chunks-1
	if last {
		size = r.plain - idx*ChunkSize
	}
	raw := r.raw[:size+tagSize]
	if _, err := r.f.ReadAt(raw, headerSize+idx*(ChunkSize+tagSize)); err != nil {
		return ErrCorrupt
	}
	plain, err := r.aead.Open(r.buf[:0], nonce(r.aead, uint64(idx)), raw, flag(last))
	if err != nil {
		r.idx = -1
		return ErrCorrupt
	}
	r.buf, r.idx = plain, idx
	return nil
}

func (r *Reader) Read(p []byte) (int, error) {
	if r.pos >= r.plain {
		return 0, io.EOF
	}
	total := 0
	for len(p) > 0 && r.pos < r.plain {
		idx := r.pos / ChunkSize
		if err := r.load(idx); err != nil {
			return total, err
		}
		n := copy(p, r.buf[r.pos-idx*ChunkSize:])
		p = p[n:]
		r.pos += int64(n)
		total += n
	}
	return total, nil
}

func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.plain + offset
	default:
		return 0, fmt.Errorf("filecrypt: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, errors.New("filecrypt: negative position")
	}
	r.pos = abs
	return abs, nil
}

func (r *Reader) Close() error { return r.f.Close() }

// Size is the plaintext size.
func (r *Reader) Size() int64 { return r.plain }
