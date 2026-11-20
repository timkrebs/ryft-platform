package appattest

import (
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// The decoder's limits. An attestation is a few KiB (API.md allows 32 KiB of
// base64 text, about 24 KiB of CBOR), an assertion a few hundred bytes; the
// deepest real nesting is 3 (the map, attStmt, x5c).
const (
	maxCBORDepth = 8
	maxCBORSize  = 32 << 10
)

// errCBOR is the decoder's only error; the verifier turns it into step "cbor".
var errCBOR = errors.New("appattest: malformed CBOR")

type cborKind uint8

const (
	cborUint  cborKind = iota + 1 // major type 0
	cborNeg                       // major type 1: the value is -1 - num
	cborBytes                     // major type 2
	cborText                      // major type 3, valid UTF-8
	cborArray                     // major type 4
	cborMap                       // major type 5, text keys, each once
)

// cborValue is one decoded item. Byte and text strings alias the input.
type cborValue struct {
	kind  cborKind
	num   uint64
	bytes []byte
	items []cborValue
	pairs []cborPair
}

type cborPair struct {
	key string
	val cborValue
}

// field returns the value of the map key name.
func (v cborValue) field(name string) (cborValue, bool) {
	if v.kind != cborMap {
		return cborValue{}, false
	}
	for _, p := range v.pairs {
		if p.key == name {
			return p.val, true
		}
	}
	return cborValue{}, false
}

// decodeCBOR decodes exactly one item that spans all of data. It accepts the
// major types 0 to 5 with definite lengths only and maps with distinct text
// keys; tags, floats, simple values, indefinite lengths, reserved additional
// information, invalid UTF-8 text, nesting deeper than maxCBORDepth, input over
// maxCBORSize and trailing bytes are refused.
func decodeCBOR(data []byte) (cborValue, error) {
	if len(data) == 0 || len(data) > maxCBORSize {
		return cborValue{}, errCBOR
	}
	d := cborDecoder{data: data}
	v, err := d.value(0)
	if err != nil {
		return cborValue{}, err
	}
	if d.off != len(d.data) {
		return cborValue{}, errCBOR
	}
	return v, nil
}

type cborDecoder struct {
	data []byte
	off  int
}

func (d *cborDecoder) left() uint64 { return uint64(len(d.data) - d.off) }

// head reads an initial byte and its argument.
func (d *cborDecoder) head() (major byte, arg uint64, err error) {
	if d.off >= len(d.data) {
		return 0, 0, errCBOR
	}
	b := d.data[d.off]
	d.off++
	major, info := b>>5, b&0x1f
	if info < 24 {
		return major, uint64(info), nil
	}
	if info > 27 { // 28 to 30 are reserved, 31 is an indefinite length
		return 0, 0, errCBOR
	}
	n := 1 << (info - 24)
	if d.left() < uint64(n) {
		return 0, 0, errCBOR
	}
	p := d.data[d.off : d.off+n]
	d.off += n
	switch n {
	case 1:
		arg = uint64(p[0])
	case 2:
		arg = uint64(binary.BigEndian.Uint16(p))
	case 4:
		arg = uint64(binary.BigEndian.Uint32(p))
	default:
		arg = binary.BigEndian.Uint64(p)
	}
	return major, arg, nil
}

// value decodes one item; depth is the number of arrays and maps around it.
func (d *cborDecoder) value(depth int) (cborValue, error) {
	major, arg, err := d.head()
	if err != nil {
		return cborValue{}, err
	}
	switch major {
	case 0:
		return cborValue{kind: cborUint, num: arg}, nil
	case 1:
		return cborValue{kind: cborNeg, num: arg}, nil
	case 2, 3:
		if arg > d.left() {
			return cborValue{}, errCBOR
		}
		b := d.data[d.off : d.off+int(arg)]
		d.off += int(arg)
		if major == 2 {
			return cborValue{kind: cborBytes, bytes: b}, nil
		}
		if !utf8.Valid(b) {
			return cborValue{}, errCBOR
		}
		return cborValue{kind: cborText, bytes: b}, nil
	case 4:
		// Every item takes at least one byte, so a count above what is left is
		// refused before anything is allocated.
		if depth >= maxCBORDepth || arg > d.left() {
			return cborValue{}, errCBOR
		}
		items := make([]cborValue, 0, int(arg))
		for range arg {
			it, err := d.value(depth + 1)
			if err != nil {
				return cborValue{}, err
			}
			items = append(items, it)
		}
		return cborValue{kind: cborArray, items: items}, nil
	case 5:
		if depth >= maxCBORDepth || arg > d.left()/2 {
			return cborValue{}, errCBOR
		}
		pairs := make([]cborPair, 0, int(arg))
		seen := make(map[string]struct{}, int(arg))
		for range arg {
			k, err := d.value(depth + 1)
			if err != nil {
				return cborValue{}, err
			}
			if k.kind != cborText {
				return cborValue{}, errCBOR
			}
			key := string(k.bytes)
			if _, dup := seen[key]; dup {
				return cborValue{}, errCBOR
			}
			seen[key] = struct{}{}
			v, err := d.value(depth + 1)
			if err != nil {
				return cborValue{}, err
			}
			pairs = append(pairs, cborPair{key: key, val: v})
		}
		return cborValue{kind: cborMap, pairs: pairs}, nil
	default: // 6 tags, 7 floats and simple values
		return cborValue{}, errCBOR
	}
}
