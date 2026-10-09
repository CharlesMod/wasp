// Package gguf reads a model's facts from its GGUF header: the architecture,
// the layer and KV geometry, the experts, and what the tensors weigh. Only the
// header is read (a few MB at the front of a file of many GB), from a stream,
// a file, or over HTTP Range requests before a byte of the weights is fetched.
package gguf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The metadata value types (gguf_metadata_value_type).
const (
	typeUint8 = iota
	typeInt8
	typeUint16
	typeInt16
	typeUint32
	typeInt32
	typeFloat32
	typeBool
	typeString
	typeArray
	typeUint64
	typeInt64
	typeFloat64
)

// Bounds past which a header is not believed. A real header holds a few
// dozen keys, a vocabulary array of 150k strings, and strings of a few KB
// (the longest, a chat template).
const (
	maxCount     = 1 << 20 // tensors, and metadata keys
	maxString    = 1 << 24
	maxArray     = 1 << 24
	maxRank      = 8
	keepIntArray = 4096 // an integer array longer than this is not geometry
)

// Kind is what a metadata value was kept as.
type Kind int

const (
	Other  Kind = iota // a float, a long array, an array of strings: walked past
	Int                // every integer width, and bool as 0 or 1
	String             // a string
	Ints               // an array of integers (or bools) short enough to be geometry
)

// Value is one metadata value, as much of it as the hive reads.
type Value struct {
	Kind Kind
	Int  uint64
	Str  string
	Ints []uint64
}

// Tensor is one entry of the tensor index.
type Tensor struct {
	Name   string
	Dims   []uint64
	Type   uint32
	Offset uint64
}

// Params is the product of the tensor's dimensions.
func (t Tensor) Params() int64 {
	n := int64(1)
	for _, d := range t.Dims {
		n *= int64(d)
	}
	return n
}

// Header is what Read returns: the version, every metadata key it kept, and
// the tensor index when it was asked for.
type Header struct {
	Version uint32
	Meta    map[string]Value
	Tensors []Tensor
}

// ErrNotGGUF is a stream that does not begin with the GGUF magic.
var ErrNotGGUF = errors.New("not a GGUF file")

// Read parses a GGUF header from r, a stream at the file's start: the
// metadata always, the tensor index when withTensors. Versions 2 and 3 only:
// v1 used 32-bit lengths throughout, nothing ships it any more, and guessing
// would misparse rather than fail.
func Read(r io.Reader, withTensors bool) (Header, error) {
	h := Header{Meta: map[string]Value{}}
	g := &reader{r: r}
	magic, err := g.read(4)
	if err != nil || string(magic) != "GGUF" {
		return h, ErrNotGGUF
	}
	if h.Version, err = g.u32(); err != nil {
		return h, err
	}
	if h.Version < 2 || h.Version > 3 {
		return h, fmt.Errorf("unsupported GGUF version %d", h.Version)
	}
	nTensors, err := g.u64()
	if err != nil {
		return h, err
	}
	nKV, err := g.u64()
	if err != nil {
		return h, err
	}
	if nTensors > maxCount || nKV > maxCount {
		return h, errors.New("implausible header counts")
	}
	for i := uint64(0); i < nKV; i++ {
		key, err := g.str()
		if err != nil {
			return h, err
		}
		t, err := g.u32()
		if err != nil {
			return h, err
		}
		v, err := g.value(t)
		if err != nil {
			return h, err
		}
		if v.Kind != Other {
			h.Meta[key] = v
		}
	}
	if !withTensors {
		return h, nil
	}
	for i := uint64(0); i < nTensors; i++ {
		var t Tensor
		if t.Name, err = g.str(); err != nil {
			return h, err
		}
		nd, err := g.u32()
		if err != nil {
			return h, err
		}
		if nd > maxRank {
			return h, errors.New("implausible tensor rank")
		}
		for d := uint32(0); d < nd; d++ {
			dim, err := g.u64()
			if err != nil {
				return h, err
			}
			t.Dims = append(t.Dims, dim)
		}
		if t.Type, err = g.u32(); err != nil {
			return h, err
		}
		if t.Offset, err = g.u64(); err != nil {
			return h, err
		}
		h.Tensors = append(h.Tensors, t)
	}
	return h, nil
}

// reader reads little-endian primitives from a stream, tracking nothing but
// position — the header is strictly sequential.
type reader struct {
	r   io.Reader
	buf [8]byte
}

func (g *reader) read(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(g.r, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (g *reader) u32() (uint32, error) {
	if _, err := io.ReadFull(g.r, g.buf[:4]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(g.buf[:4]), nil
}

func (g *reader) u64() (uint64, error) {
	if _, err := io.ReadFull(g.r, g.buf[:8]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(g.buf[:8]), nil
}

func (g *reader) str() (string, error) {
	n, err := g.u64()
	if err != nil {
		return "", err
	}
	if n > maxString {
		return "", fmt.Errorf("implausible string length %d", n)
	}
	b, err := g.read(int(n))
	return string(b), err
}

// value reads one metadata value: integers (bools as 0 or 1), strings, and
// integer arrays short enough to be geometry are kept; floats and every other
// array — a vocabulary is 150k strings — are walked past without being held.
func (g *reader) value(t uint32) (Value, error) {
	switch t {
	case typeUint8, typeInt8, typeBool:
		b, err := g.read(1)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: Int, Int: uint64(b[0])}, nil
	case typeUint16, typeInt16:
		b, err := g.read(2)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: Int, Int: uint64(binary.LittleEndian.Uint16(b))}, nil
	case typeUint32, typeInt32:
		v, err := g.u32()
		return Value{Kind: Int, Int: uint64(v)}, err
	case typeUint64, typeInt64:
		v, err := g.u64()
		return Value{Kind: Int, Int: v}, err
	case typeFloat32:
		_, err := g.read(4)
		return Value{}, err
	case typeFloat64:
		_, err := g.read(8)
		return Value{}, err
	case typeString:
		s, err := g.str()
		return Value{Kind: String, Str: s}, err
	case typeArray:
		et, err := g.u32()
		if err != nil {
			return Value{}, err
		}
		n, err := g.u64()
		if err != nil {
			return Value{}, err
		}
		if n > maxArray {
			return Value{}, fmt.Errorf("implausible array length %d", n)
		}
		out := Value{Kind: Ints}
		keep := n <= keepIntArray
		for i := uint64(0); i < n; i++ {
			v, err := g.value(et)
			if err != nil {
				return Value{}, err
			}
			if keep && v.Kind == Int {
				out.Ints = append(out.Ints, v.Int)
			}
		}
		if len(out.Ints) == 0 {
			return Value{}, nil
		}
		return out, nil
	}
	return Value{}, fmt.Errorf("unknown gguf value type %d", t)
}

// typeSize is a ggml tensor type's block size (elements) and bytes per block.
type typeSize struct{ block, bytes int64 }

// ggmlTypes holds the sizes of the tensor types in common GGUF files, by
// ggml_type number.
var ggmlTypes = map[uint32]typeSize{
	0: {1, 4}, 1: {1, 2}, 2: {32, 18}, 3: {32, 20}, 6: {32, 22}, 7: {32, 24},
	8: {32, 34}, 9: {32, 36}, 10: {256, 84}, 11: {256, 110}, 12: {256, 144},
	13: {256, 176}, 14: {256, 210}, 15: {256, 292}, 16: {256, 66}, 17: {256, 74},
	18: {256, 98}, 19: {256, 50}, 20: {32, 18}, 21: {256, 110}, 22: {256, 82},
	23: {256, 136}, 24: {1, 1}, 25: {1, 2}, 26: {1, 4}, 27: {1, 8}, 28: {1, 8},
	29: {256, 56}, 30: {1, 2}, 34: {256, 54}, 35: {256, 66}, 39: {32, 17},
}

// Bytes is what the tensor weighs in the file, or 0 for a type this reader
// does not know (the caller then falls back to the file size).
func (t Tensor) Bytes() int64 {
	ts, ok := ggmlTypes[t.Type]
	if !ok {
		return 0
	}
	return t.Params() / ts.block * ts.bytes
}
