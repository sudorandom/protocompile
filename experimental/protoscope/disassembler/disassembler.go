// Copyright 2020-2026 Buf Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the \"License\");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an \"AS IS\" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package disassembler

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Options contains disassembly options.
type Options struct {
	ExplicitWireTypes      bool
	ExplicitLengthPrefixes bool
	NoGroups               bool
}

// Disassemble translates Protobuf wire format into protoscope text.
func Disassemble(data []byte, out io.Writer) error {
	return DisassembleWithOptions(data, out, Options{})
}

// DisassembleWithOptions translates Protobuf wire format into protoscope text with options.
func DisassembleWithOptions(data []byte, out io.Writer, opts Options) error {
	d := &disassembler{data: data, opts: opts}
	return d.disassemble(out, 0, 0, 0)
}

type disassembler struct {
	data []byte
	off  int
	opts Options
}

const (
	wireVarint = 0
	wireI64    = 1
	wireLen    = 2
	wireSGroup = 3
	wireEGroup = 4
	wireI32    = 5

	maxDepth = 10
)

func (d *disassembler) disassemble(out io.Writer, indent int, groupTag uint64, depth int) error {
	if depth > maxDepth {
		return errors.New("max depth exceeded")
	}

	for d.off < len(d.data) {
		u, n := binary.Uvarint(d.data[d.off:])
		if n <= 0 {
			// Not a valid varint, dump remaining as hex
			return d.dumpHex(out, indent)
		}

		tag := u >> 3
		wireType := u & 0x7

		// If we're in a group and see an EGroup with the same tag, we're done.
		if groupTag != 0 && wireType == wireEGroup && tag == groupTag {
			if !d.opts.NoGroups {
				d.off += n
				return nil
			}
		}

		if wireType > 5 {
			// Invalid wire type, this isn't a protobuf stream or it's corrupted.
			return d.dumpHex(out, indent)
		}

		fmt.Fprint(out, strings.Repeat("  ", indent))
		fmt.Fprintf(out, "%d:", tag)
		if d.opts.ExplicitWireTypes {
			switch wireType {
			case wireVarint:
				fmt.Fprint(out, "VARINT ")
			case wireI64:
				fmt.Fprint(out, "I64 ")
			case wireLen:
				fmt.Fprint(out, "LEN ")
			case wireSGroup:
				fmt.Fprint(out, "SGROUP ")
			case wireEGroup:
				fmt.Fprint(out, "EGROUP ")
			case wireI32:
				fmt.Fprint(out, "I32 ")
			}
		} else {
			fmt.Fprint(out, " ")
		}
		d.off += n

		switch wireType {
		case wireVarint:
			v, n := binary.Uvarint(d.data[d.off:])
			if n <= 0 {
				return fmt.Errorf("invalid varint at offset %d", d.off)
			}
			d.off += n
			fmt.Fprintf(out, "%d\n", v)

		case wireI64:
			if d.off+8 > len(d.data) {
				return errors.New("unexpected EOF reading I64")
			}
			v := binary.LittleEndian.Uint64(d.data[d.off:])
			d.off += 8
			fmt.Fprintf(out, "0x%016xi64\n", v)

		case wireLen:
			l, n := binary.Uvarint(d.data[d.off:])
			if n <= 0 {
				return fmt.Errorf("invalid length at offset %d", d.off)
			}
			d.off += n
			if l > uint64(len(d.data)-d.off) {
				return fmt.Errorf("length %d out of bounds", l)
			}
			payload := d.data[d.off : d.off+int(l)]
			d.off += int(l)

			if d.opts.ExplicitLengthPrefixes {
				fmt.Fprintf(out, "%d ", l)
			}

			// Heuristic: Prefer string if it's cleanly printable and not obviously a message.
			switch {
			case isPrintable(payload) && !isMessage(payload):
				fmt.Fprintf(out, "{%q}\n", string(payload))
			case isMessage(payload):
				fmt.Fprint(out, "{\n")
				sub := &disassembler{data: payload, opts: d.opts}
				if err := sub.disassemble(out, indent+1, 0, depth+1); err != nil {
					// If recursion fails, fall back to hex for this payload
					fmt.Fprintf(out, " (fallback) `")
					for i, b := range payload {
						if i > 0 {
							fmt.Fprint(out, " ")
						}
						fmt.Fprintf(out, "%02x", b)
					}
					fmt.Fprint(out, "`")
				}
				fmt.Fprint(out, strings.Repeat("  ", indent))
				fmt.Fprint(out, "}\n")
			default:
				fmt.Fprintf(out, "{`")
				for i, b := range payload {
					if i > 0 {
						fmt.Fprint(out, " ")
					}
					fmt.Fprintf(out, "%02x", b)
				}
				fmt.Fprint(out, "`}\n")
			}

		case wireSGroup:
			if d.opts.NoGroups {
				fmt.Fprint(out, "\n")
				continue
			}
			fmt.Fprint(out, "!{\n")
			if err := d.disassemble(out, indent+1, tag, depth+1); err != nil {
				return err
			}
			fmt.Fprint(out, strings.Repeat("  ", indent))
			fmt.Fprint(out, "}\n")

		case wireEGroup:
			if d.opts.NoGroups {
				fmt.Fprint(out, "\n")
				continue
			}
			// Should have been handled above if matching.
			fmt.Fprintf(out, "(unmatched EGroup)\n")

		case wireI32:
			if d.off+4 > len(d.data) {
				return errors.New("unexpected EOF reading I32")
			}
			v := binary.LittleEndian.Uint32(d.data[d.off:])
			d.off += 4
			fmt.Fprintf(out, "0x%08xi32\n", v)

		default:
			fmt.Fprintf(out, "(unsupported wire type %d)\n", wireType)
		}
	}
	return nil
}

func (d *disassembler) dumpHex(out io.Writer, indent int) error {
	if d.off >= len(d.data) {
		return nil
	}
	fmt.Fprint(out, strings.Repeat("  ", indent))
	fmt.Fprint(out, "`")
	for i, b := range d.data[d.off:] {
		if i > 0 {
			fmt.Fprint(out, " ")
		}
		fmt.Fprintf(out, "%02x", b)
	}
	fmt.Fprint(out, "`\n")
	d.off = len(d.data)
	return nil
}

func isMessage(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	off := 0
	fields := 0
	for off < len(data) {
		u, n := binary.Uvarint(data[off:])
		if n <= 0 {
			return false
		}
		off += n
		wireType := u & 0x7
		tag := u >> 3
		if wireType > 5 || tag == 0 {
			return false
		}
		fields++
		switch wireType {
		case wireVarint:
			_, n = binary.Uvarint(data[off:])
			if n <= 0 {
				return false
			}
			off += n
		case wireI64:
			if off+8 > len(data) {
				return false
			}
			off += 8
		case wireLen:
			l, n := binary.Uvarint(data[off:])
			if n <= 0 {
				return false
			}
			off += n
			if l > uint64(len(data)-off) {
				return false
			}
			off += int(l)
		case wireSGroup:
			// Simple check: groups must be finite
			return false
		case wireEGroup:
			return false
		case wireI32:
			if off+4 > len(data) {
				return false
			}
			off += 4
		default:
			return false
		}
	}

	if fields > 0 && off == len(data) {
		// Heuristic: if it has many fields, it's likely a message even if it looks like a string.
		if fields > 3 {
			return true
		}
		// If it's short and mostly printable, it's likely a string.
		if isMostlyPrintable(data) {
			return false
		}
		return true
	}
	return false
}

func isPrintable(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if !utf8.Valid(data) {
		return false
	}
	for _, b := range data {
		if !unicode.IsPrint(rune(b)) && !unicode.IsSpace(rune(b)) {
			return false
		}
	}
	return true
}

func isMostlyPrintable(data []byte) bool {
	printable := 0
	for _, b := range data {
		if unicode.IsPrint(rune(b)) || unicode.IsSpace(rune(b)) {
			printable++
		}
	}
	return printable*10 > len(data)*8 // > 80%
}
