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

package assembler

import (
	"encoding/binary"

	"github.com/bufbuild/protocompile/experimental/id"
	"github.com/bufbuild/protocompile/experimental/protoscope/ast"
	"github.com/bufbuild/protocompile/experimental/seq"
	"github.com/bufbuild/protocompile/experimental/token"
	"github.com/bufbuild/protocompile/experimental/token/keyword"
)

// Assemble translates a protoscope AST into Protobuf wire format.
func Assemble(file *ast.File) []byte {
	a := &assembler{}
	for decl := range seq.Values(file.Decls()) {
		a.assembleDecl(decl)
	}
	return a.buf
}

type assembler struct {
	buf []byte
}

func (a *assembler) assembleDecl(decl ast.DeclAny) {
	switch decl.Kind() {
	case ast.DeclKindField:
		a.assembleField(id.Wrap(decl.Context(), id.ID[ast.Field](decl.ID().Value())))
	case ast.DeclKindLiteral:
		a.assembleLiteral(id.Wrap(decl.Context(), id.ID[ast.Literal](decl.ID().Value())))
	case ast.DeclKindBlock:
		a.assembleBlock(id.Wrap(decl.Context(), id.ID[ast.Block](decl.ID().Value())))
	}
}

func (a *assembler) assembleField(f ast.Field) {
	tag, _ := f.Tag().AsNumber().Int()

	// Heuristic for wire type if not specified.
	wireType := uint64(0)
	val := f.Value()
	if !val.IsZero() && val.Kind() == ast.DeclKindBlock {
		block := id.Wrap(val.Context(), id.ID[ast.Block](val.ID().Value()))
		if block.Token().Keyword() == keyword.Bang {
			wireType = 3 // SGROUP
		} else {
			wireType = 2 // LEN
		}
	}

	// Override with explicit wire type hint.
	switch f.WireType().Text() {
	case "VARINT":
		wireType = 0
	case "I64":
		wireType = 1
	case "LEN":
		wireType = 2
	case "SGROUP":
		wireType = 3
	case "EGROUP":
		wireType = 4
	case "I32":
		wireType = 5
	}

	a.writeVarint(tag<<3 | wireType)

	if wireType == 4 { // EGROUP
		return
	}

	if wireType == 3 { // SGROUP
		a.assembleDecl(val)
		a.writeVarint(tag<<3 | 4) // Emit matching EGROUP
		return
	}

	a.assembleDecl(val)
}

func (a *assembler) assembleLiteral(l ast.Literal) {
	tok := l.Token()
	switch tok.Kind() {
	case token.Number:
		// Check for suffix hints
		switch {
		case tok.AsNumber().Suffix().Text() == "i32":
			v, _ := tok.AsNumber().Int()
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(v))
			a.buf = append(a.buf, buf[:]...)
		case tok.AsNumber().Suffix().Text() == "i64":
			v, _ := tok.AsNumber().Int()
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], v)
			a.buf = append(a.buf, buf[:]...)
		default:
			v, _ := tok.AsNumber().Int()
			a.writeVarint(v)
		}
	case token.String:
		s := tok.AsString().Text()
		a.writeVarint(uint64(len(s)))
		a.buf = append(a.buf, s...)
	}
}

func (a *assembler) assembleBlock(b ast.Block) {
	tok := b.Token()
	switch tok.Keyword() {
	case keyword.LBracket:
		// Length-prefixed block
		sub := &assembler{}
		for decl := range seq.Values(b.Decls()) {
			sub.assembleDecl(decl)
		}
		a.writeVarint(uint64(len(sub.buf)))
		a.buf = append(a.buf, sub.buf...)
	case keyword.Bang:
		// Group content (no length prefix)
		for decl := range seq.Values(b.Decls()) {
			a.assembleDecl(decl)
		}
	}
}

func (a *assembler) writeVarint(v uint64) {
	var buf [10]byte
	n := binary.PutUvarint(buf[:], v)
	a.buf = append(a.buf, buf[:n]...)
}
