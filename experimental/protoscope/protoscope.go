// Copyright 2020-2026 Buf Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package protoscope

import (
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bufbuild/protocompile/experimental/internal/protoscope/assembler"
	"github.com/bufbuild/protocompile/experimental/internal/protoscope/ast"
	"github.com/bufbuild/protocompile/experimental/internal/protoscope/disassembler"
	"github.com/bufbuild/protocompile/experimental/internal/protoscope/parser"
	"github.com/bufbuild/protocompile/experimental/report"
	"github.com/bufbuild/protocompile/experimental/seq"
	"github.com/bufbuild/protocompile/experimental/source"
	"github.com/bufbuild/protocompile/experimental/source/length"
	"github.com/bufbuild/protocompile/experimental/token"
)

// Severity represents diagnostic severity levels.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityError
)

// Position represents a 1-indexed line and column position.
type Position struct {
	Line, Column int
}

// Range represents a span between two positions.
type Range struct {
	Start, End Position
}

// Diagnostic represents a syntax or validation diagnostic.
type Diagnostic struct {
	Range   Range
	Message string
	Level   Severity
}

// DisassembleOptions matches the internal disassembler options.
type DisassembleOptions struct {
	ExplicitWireTypes      bool
	ExplicitLengthPrefixes bool
	NoGroups               bool
	MaxDepth               int
}

// Assemble parses and compiles protoscope text directly to protobuf wire binary.
func Assemble(path string, text []byte) ([]byte, []Diagnostic) {
	src := source.NewFile(path, string(text))
	r := &report.Report{}
	file, ok := parser.Parse(path, src, r)
	diags := convertDiagnostics(r)
	if !ok || file == nil {
		return nil, diags
	}
	out := assembler.Assemble(file)
	return out, diags
}

// Disassemble converts protobuf wire binary back to protoscope text.
func Disassemble(data []byte, opts DisassembleOptions) (string, error) {
	var buf strings.Builder
	disOpts := disassembler.Options{
		ExplicitWireTypes:      opts.ExplicitWireTypes,
		ExplicitLengthPrefixes: opts.ExplicitLengthPrefixes,
		NoGroups:               opts.NoGroups,
		MaxDepth:               opts.MaxDepth,
	}
	err := disassembler.DisassembleWithOptions(data, &buf, disOpts)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Diagnostics parses the text and returns any syntactic or structural diagnostics.
func Diagnostics(path string, text []byte) []Diagnostic {
	src := source.NewFile(path, string(text))
	r := &report.Report{}
	_, _ = parser.Parse(path, src, r)
	return convertDiagnostics(r)
}

// DocumentSymbol represents a simplified symbol hierarchy (e.g. fields, groups, blocks).
type DocumentSymbol struct {
	Name     string
	Detail   string
	Kind     string // e.g., "field", "group", "block", "literal"
	Range    Range
	Children []DocumentSymbol
}

// DocumentSymbols returns a hierarchy of symbols within the protoscope file.
func DocumentSymbols(path string, text []byte) ([]DocumentSymbol, []Diagnostic) {
	src := source.NewFile(path, string(text))
	r := &report.Report{}
	file, ok := parser.Parse(path, src, r)
	diags := convertDiagnostics(r)
	if !ok || file == nil {
		return nil, diags
	}
	var symbols []DocumentSymbol
	for decl := range seq.Values(file.Decls()) {
		symbols = append(symbols, collectSymbols(decl)...)
	}
	return symbols, diags
}

// HoverInfo holds information to display on hover.
type HoverInfo struct {
	Range Range
	Text  string // Markdown formatted hover info
}

// Hover returns hover documentation for the token/node at the given line/column.
func Hover(path string, text []byte, line, col int) (*HoverInfo, error) {
	src := source.NewFile(path, string(text))
	r := &report.Report{}
	file, _ := parser.Parse(path, src, r)
	if file == nil {
		return nil, nil
	}

	loc := src.InverseLocation(line, col, length.UTF16)
	offset := loc.Offset

	node := findNode(file, offset)
	if node.IsZero() {
		return nil, nil
	}

	hover := &HoverInfo{
		Range: convertSpan(node.Span()),
	}

	switch node.Kind() {
	case ast.DeclKindField:
		f := node.AsField()
		var sb strings.Builder
		sb.WriteString("### Field Tag\n")
		fmt.Fprintf(&sb, "- **Field Number:** `%s`\n", f.Tag().Text())
		if wt := f.WireType(); !wt.IsZero() && wt.Text() != "" {
			fmt.Fprintf(&sb, "- **Wire Type:** `%s`\n", wt.Text())
		}
		hover.Text = sb.String()

	case ast.DeclKindLiteral:
		l := node.AsLiteral()
		tok := l.Token()
		var sb strings.Builder
		sb.WriteString("### Literal Value\n")

		if tok.Kind() == token.Number {
			fmt.Fprintf(&sb, "- **Raw Text:** `%s`\n", tok.Text())
			num := tok.AsNumber()
			fmt.Fprintf(&sb, "- **Type:** `Number` (suffix: `%s`)\n", num.Suffix().Text())
			if v, exact := num.Int(); exact {
				fmt.Fprintf(&sb, "- **Decimal:** `%d`\n", v)
				fmt.Fprintf(&sb, "- **Hexadecimal:** `0x%X`\n", v)
				fmt.Fprintf(&sb, "- **Binary:** `0b%b`\n", v)
				fmt.Fprintf(&sb, "- **As Varint Bytes:** `%s`\n", varintBytes(v))

				// Interpret as signed 64-bit to show zigzag encoding if applicable
				sval := int64(v)
				fmt.Fprintf(&sb, "- **Zigzag Encoded:** `%d`\n", (sval<<1)^(sval>>63))
			} else if fval, exactf := num.Float(); exactf {
				fmt.Fprintf(&sb, "- **Floating Point:** `%g`\n", fval)
			}
		} else if tok.Kind() == token.String {
			sToken := tok.AsString()
			open, _ := sToken.Quotes()
			if open.Text() == "`" {
				fmt.Fprintf(&sb, "- **Raw Hex:** `%s`\n", tok.Text())
			} else {
				fmt.Fprintf(&sb, "- **Raw Text:** `%s`\n", tok.Text())
			}
			sb.WriteString("- **Type:** `String`\n")
			if open.Text() == "`" {
				// Hex string literal
				decoded, err := hexDecode(tok.Text())
				if err == nil {
					fmt.Fprintf(&sb, "- **Hex Length:** `%d bytes`\n", len(decoded))
					if isPrintable(decoded) {
						fmt.Fprintf(&sb, "- **Decoded Text:** `%s`\n", string(decoded))
					} else {
						fmt.Fprintf(&sb, "- **Decoded Hex Bytes:** `%02X`\n", decoded)
					}
				}
			}
		}
		hover.Text = sb.String()

	case ast.DeclKindBlock:
		b := node.AsBlock()
		var sb strings.Builder
		name := b.Token().Text()
		if name == "!{" {
			sb.WriteString("### Group Block\n")
			sb.WriteString("Represents a deprecated Protobuf Group wire format structure (`!{ ... }`).")
		} else {
			sb.WriteString("### Length-Prefixed Block\n")
			sb.WriteString("Represents a length-delimited payload (`{ ... }`), such as a submessage, packed repeated field, or raw string/bytes.")
		}
		hover.Text = sb.String()
	}

	return hover, nil
}

func collectSymbols(decl ast.DeclAny) []DocumentSymbol {
	if decl.IsZero() {
		return nil
	}
	switch decl.Kind() {
	case ast.DeclKindField:
		f := decl.AsField()
		tagText := f.Tag().Text()

		var children []DocumentSymbol
		val := f.Value()
		if !val.IsZero() {
			children = collectSymbols(val)
		}

		detail := ""
		if wt := f.WireType(); !wt.IsZero() && wt.Text() != "" {
			detail = ":" + wt.Text()
		}

		return []DocumentSymbol{{
			Name:     tagText + ":",
			Detail:   detail,
			Kind:     "field",
			Range:    convertSpan(f.Span()),
			Children: children,
		}}

	case ast.DeclKindLiteral:
		l := decl.AsLiteral()
		return []DocumentSymbol{{
			Name:  l.Token().Text(),
			Kind:  "literal",
			Range: convertSpan(l.Span()),
		}}

	case ast.DeclKindBlock:
		b := decl.AsBlock()
		var children []DocumentSymbol
		for child := range seq.Values(b.Decls()) {
			children = append(children, collectSymbols(child)...)
		}

		name := b.Token().Text()
		detail := ""
		switch name {
		case "!{":
			name = "Group"
			detail = "!{}"
		case "{":
			name = "Length-Prefixed"
			detail = "{}"
		}

		return []DocumentSymbol{{
			Name:     name,
			Detail:   detail,
			Kind:     "block",
			Range:    convertSpan(b.Span()),
			Children: children,
		}}
	}
	return nil
}

func convertSpan(span source.Span) Range {
	if span.IsZero() {
		return Range{}
	}
	startLoc := span.StartLoc()
	endLoc := span.EndLoc()
	return Range{
		Start: Position{Line: startLoc.Line, Column: startLoc.Column},
		End:   Position{Line: endLoc.Line, Column: endLoc.Column},
	}
}

func varintBytes(v uint64) string {
	var buf []string
	for v >= 0x80 {
		buf = append(buf, fmt.Sprintf("%02X", byte(v|0x80)))
		v >>= 7
	}
	buf = append(buf, fmt.Sprintf("%02X", byte(v)))
	return strings.Join(buf, " ")
}

func isPrintable(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError {
			return false
		}
		if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return false
		}
		data = data[size:]
	}
	return true
}

func hexDecode(text string) ([]byte, error) {
	// Strip backticks
	text = strings.Trim(text, "`")
	// Remove whitespace
	var cleaned strings.Builder
	for _, r := range text {
		if !unicode.IsSpace(r) {
			cleaned.WriteRune(r)
		}
	}
	return hex.DecodeString(cleaned.String())
}

func findNode(file *ast.File, offset int) ast.DeclAny {
	var best ast.DeclAny
	var search func(decl ast.DeclAny)
	search = func(decl ast.DeclAny) {
		if decl.IsZero() {
			return
		}
		span := decl.Span()
		if span.IsZero() {
			return
		}
		if offset >= span.Start && offset <= span.End {
			best = decl
			if decl.Kind() == ast.DeclKindBlock {
				for child := range seq.Values(decl.AsBlock().Decls()) {
					search(child)
				}
			} else if decl.Kind() == ast.DeclKindField {
				search(decl.AsField().Value())
			}
		}
	}
	for decl := range seq.Values(file.Decls()) {
		search(decl)
	}
	return best
}

func convertDiagnostics(r *report.Report) []Diagnostic {
	diagnostics := make([]Diagnostic, 0, len(r.Diagnostics))
	for _, diag := range r.Diagnostics {
		severity := SeverityError
		switch diag.Level() {
		case report.Warning:
			severity = SeverityWarning
		case report.Remark:
			severity = SeverityInfo
		}

		span := diag.Primary()
		var rangeVal Range
		if !span.IsZero() {
			startLoc := span.StartLoc()
			endLoc := span.EndLoc()
			rangeVal = Range{
				Start: Position{Line: startLoc.Line, Column: startLoc.Column},
				End:   Position{Line: endLoc.Line, Column: endLoc.Column},
			}
		}

		diagnostics = append(diagnostics, Diagnostic{
			Range:   rangeVal,
			Message: diag.Message(),
			Level:   severity,
		})
	}
	return diagnostics
}

// Representation represents a possible translation/formatting of a protobuf value.
type Representation struct {
	Type        string  // E.g., "message", "string", "bytes", "varint", "zigzag", "bool", "fixed32", "float32", "fixed64", "float64", "packed_varint", "packed_fixed32", "packed_fixed64"
	Text        string  // The protoscope textual value representation
	Description string  // Human-readable description
	Likelihood  float64 // Likelihood score, between 0.0 and 1.0 (higher is more likely)
}

func mapRepresentations(internalReps []disassembler.Representation) []Representation {
	if internalReps == nil {
		return nil
	}
	reps := make([]Representation, len(internalReps))
	for i, r := range internalReps {
		reps[i] = Representation{
			Type:        r.Type,
			Text:        r.Text,
			Description: r.Description,
			Likelihood:  r.Likelihood,
		}
	}
	return reps
}

// Possibilities analyzes the raw payload bytes for a given wire type and returns
// all valid alternative representations sorted by likelihood.
func Possibilities(wireType int, payload []byte) []Representation {
	return mapRepresentations(disassembler.Possibilities(wireType, payload))
}
