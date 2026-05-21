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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
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

// Representation represents a possible translation/formatting of a protobuf value.
type Representation struct {
	Type        string  // E.g., "message", "string", "bytes", "varint", "zigzag", "bool", "fixed32", "float32", "fixed64", "float64", "packed_varint", "packed_fixed32", "packed_fixed64"
	Text        string  // The protoscope textual value representation
	Description string  // Human-readable description
	Likelihood  float64 // Likelihood score, between 0.0 and 1.0 (higher is more likely)
}

// Possibilities analyzes the raw payload bytes for a given wire type and returns
// all valid alternative representations sorted by likelihood.
func Possibilities(wireType int, payload []byte) []Representation {
	var reps []Representation

	switch wireType {
	case wireVarint:
		val, n := binary.Uvarint(payload)
		if n <= 0 || n < len(payload) {
			return nil
		}

		// 1. Unsigned Varint (Decimal)
		reps = append(reps, Representation{
			Type:        "varint",
			Text:        strconv.FormatUint(val, 10),
			Description: "Varint",
			Likelihood:  0.9,
		})

		// 2. Zigzag Varint
		zz := int64(val>>1) ^ -int64(val&1)
		reps = append(reps, Representation{
			Type:        "zigzag",
			Text:        strconv.FormatInt(zz, 10),
			Description: "Zigzag Varint",
			Likelihood:  0.7,
		})

		// 3. Boolean
		if val == 0 {
			reps = append(reps, Representation{
				Type:        "bool",
				Text:        "false",
				Description: "Boolean",
				Likelihood:  0.8,
			})
		} else if val == 1 {
			reps = append(reps, Representation{
				Type:        "bool",
				Text:        "true",
				Description: "Boolean",
				Likelihood:  0.8,
			})
		}

	case wireI32:
		if len(payload) != 4 {
			return nil
		}
		val := binary.LittleEndian.Uint32(payload)

		// 1. Fixed32 (Hex)
		reps = append(reps, Representation{
			Type:        "fixed32",
			Text:        fmt.Sprintf("0x%08xi32", val),
			Description: "Fixed32 (Hex)",
			Likelihood:  0.9,
		})

		// 2. Fixed32 (Decimal)
		reps = append(reps, Representation{
			Type:        "fixed32",
			Text:        fmt.Sprintf("%di32", int32(val)),
			Description: "Fixed32 (Decimal)",
			Likelihood:  0.8,
		})

		// 3. Float32
		fval := math.Float32frombits(val)
		f64 := float64(fval)
		text := fmt.Sprintf("%gf32", fval)
		if !strings.Contains(text, ".") && !strings.Contains(text, "e") && !math.IsNaN(f64) && !math.IsInf(f64, 0) {
			text = fmt.Sprintf("%.1ff32", fval)
		}
		var likelihood float64
		switch {
		case math.IsNaN(f64) || math.IsInf(f64, 0):
			likelihood = 0.2
		case fval == 0.0 || (math.Abs(f64) > 1e-6 && math.Abs(f64) < 1e6):
			likelihood = 0.7
		default:
			likelihood = 0.5
		}
		reps = append(reps, Representation{
			Type:        "float32",
			Text:        text,
			Description: "Float32",
			Likelihood:  likelihood,
		})

	case wireI64:
		if len(payload) != 8 {
			return nil
		}
		val := binary.LittleEndian.Uint64(payload)

		// 1. Fixed64 (Hex)
		reps = append(reps, Representation{
			Type:        "fixed64",
			Text:        fmt.Sprintf("0x%016xi64", val),
			Description: "Fixed64 (Hex)",
			Likelihood:  0.9,
		})

		// 2. Fixed64 (Decimal)
		reps = append(reps, Representation{
			Type:        "fixed64",
			Text:        fmt.Sprintf("%di64", int64(val)),
			Description: "Fixed64 (Decimal)",
			Likelihood:  0.8,
		})

		// 3. Float64
		fval := math.Float64frombits(val)
		text := fmt.Sprintf("%gf64", fval)
		if !strings.Contains(text, ".") && !strings.Contains(text, "e") && !math.IsNaN(fval) && !math.IsInf(fval, 0) {
			text = fmt.Sprintf("%.1ff64", fval)
		}
		var likelihood float64
		switch {
		case math.IsNaN(fval) || math.IsInf(fval, 0):
			likelihood = 0.2
		case fval == 0.0 || (math.Abs(fval) > 1e-6 && math.Abs(fval) < 1e6):
			likelihood = 0.7
		default:
			likelihood = 0.5
		}
		reps = append(reps, Representation{
			Type:        "float64",
			Text:        text,
			Description: "Float64",
			Likelihood:  likelihood,
		})

	case wireLen:
		// 1. Fallback Hex Bytes (always valid)
		var hexSb strings.Builder
		hexSb.WriteString("{`")
		for i, b := range payload {
			if i > 0 {
				hexSb.WriteByte(' ')
			}
			fmt.Fprintf(&hexSb, "%02x", b)
		}
		hexSb.WriteString("`}")
		reps = append(reps, Representation{
			Type:        "bytes",
			Text:        hexSb.String(),
			Description: "Bytes",
			Likelihood:  0.1,
		})

		// 2. String
		if utf8.Valid(payload) {
			isMsg := isMessage(payload)
			likelihood := 0.4
			if isPrintable(payload) {
				if !isMsg {
					likelihood = 0.9
				} else {
					likelihood = 0.6
				}
			}
			reps = append(reps, Representation{
				Type:        "string",
				Text:        fmt.Sprintf("{%q}", string(payload)),
				Description: "String",
				Likelihood:  likelihood,
			})
		}

		// 3. Message
		// Check structural validity as message
		isStructMessage := false
		if len(payload) > 0 {
			off := 0
			fields := 0
			ok := true
			for off < len(payload) {
				u, n := binary.Uvarint(payload[off:])
				if n <= 0 {
					ok = false
					break
				}
				off += n
				wType := u & 0x7
				tag := u >> 3
				if wType > 5 || tag == 0 {
					ok = false
					break
				}
				fields++
				switch wType {
				case wireVarint:
					_, n = binary.Uvarint(payload[off:])
					if n <= 0 {
						ok = false
						break
					}
					off += n
				case wireI64:
					if off+8 > len(payload) {
						ok = false
						break
					}
					off += 8
				case wireLen:
					l, n := binary.Uvarint(payload[off:])
					if n <= 0 {
						ok = false
						break
					}
					off += n
					if l > uint64(len(payload)-off) {
						ok = false
						break
					}
					off += int(l)
				case wireSGroup:
					ok = false // groups not supported for simple structural check here
				case wireEGroup:
					ok = false
				case wireI32:
					if off+4 > len(payload) {
						ok = false
						break
					}
					off += 4
				default:
					ok = false
				}
				if !ok {
					break
				}
			}
			isStructMessage = ok && fields > 0 && off == len(payload)
		}

		if isStructMessage {
			var buf bytes.Buffer
			if err := Disassemble(payload, &buf); err == nil {
				text := strings.TrimSpace(buf.String())
				lines := strings.Split(text, "\n")
				var cleaned []string
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line != "" {
						cleaned = append(cleaned, line)
					}
				}
				text = "{ " + strings.Join(cleaned, " ") + " }"
				likelihood := 0.6
				if isMessage(payload) {
					likelihood = 0.9
				}
				reps = append(reps, Representation{
					Type:        "message",
					Text:        text,
					Description: "Embedded Message",
					Likelihood:  likelihood,
				})
			}
		}

		// 4. Packed Varints
		if len(payload) > 0 {
			var ints []uint64
			off := 0
			ok := true
			for off < len(payload) {
				v, n := binary.Uvarint(payload[off:])
				if n <= 0 {
					ok = false
					break
				}
				off += n
				ints = append(ints, v)
			}
			if ok && len(ints) > 0 {
				var sb strings.Builder
				sb.WriteString("[")
				for _, val := range ints {
					fmt.Fprintf(&sb, " %d", val)
				}
				sb.WriteString(" ]")
				allSmall := true
				for _, val := range ints {
					if val > 1000 {
						allSmall = false
						break
					}
				}
				likelihood := 0.3
				if allSmall {
					likelihood = 0.5
				}
				reps = append(reps, Representation{
					Type:        "packed_varint",
					Text:        sb.String(),
					Description: "Packed Varints",
					Likelihood:  likelihood,
				})
			}
		}

		// 5. Packed Fixed32
		if len(payload) > 0 && len(payload)%4 == 0 {
			var sb strings.Builder
			sb.WriteString("[")
			for i := 0; i < len(payload); i += 4 {
				v := binary.LittleEndian.Uint32(payload[i:])
				fmt.Fprintf(&sb, " 0x%08xi32", v)
			}
			sb.WriteString(" ]")
			reps = append(reps, Representation{
				Type:        "packed_fixed32",
				Text:        sb.String(),
				Description: "Packed Fixed32",
				Likelihood:  0.4,
			})
		}

		// 6. Packed Fixed64
		if len(payload) > 0 && len(payload)%8 == 0 {
			var sb strings.Builder
			sb.WriteString("[")
			for i := 0; i < len(payload); i += 8 {
				v := binary.LittleEndian.Uint64(payload[i:])
				fmt.Fprintf(&sb, " 0x%016xi64", v)
			}
			sb.WriteString(" ]")
			reps = append(reps, Representation{
				Type:        "packed_fixed64",
				Text:        sb.String(),
				Description: "Packed Fixed64",
				Likelihood:  0.4,
			})
		}
	}

	sort.Slice(reps, func(i, j int) bool {
		if reps[i].Likelihood == reps[j].Likelihood {
			return reps[i].Description < reps[j].Description
		}
		return reps[i].Likelihood > reps[j].Likelihood
	})

	return reps
}
