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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssembleAndDisassemble(t *testing.T) {
	input := `1: 150
2: {
  1: "hello"
}
`
	binary, diags := Assemble("test.protoscope", []byte(input))
	require.Empty(t, diags)
	require.NotEmpty(t, binary)

	// Verify disassembled output matches input
	text, err := Disassemble(binary, DisassembleOptions{})
	require.NoError(t, err)
	assert.Contains(t, text, "1: 150")
	assert.Contains(t, text, `2: {`+"`"+`0a 05 68 65 6c 6c 6f`+"`"+`}`)

	// Test MaxDepth
	nestedInput := `1: {
  2: {
    3: 150
  }
}
`
	nestedBinary, nestedDiags := Assemble("nested.protoscope", []byte(nestedInput))
	require.Empty(t, nestedDiags)
	require.NotEmpty(t, nestedBinary)

	// Disassembling with default options should work
	_, err = Disassemble(nestedBinary, DisassembleOptions{})
	require.NoError(t, err)

	// Disassembling with MaxDepth = 1 should fail
	_, err = Disassemble(nestedBinary, DisassembleOptions{MaxDepth: 1})
	require.Error(t, err)
	assert.Equal(t, "max depth exceeded", err.Error())
}

func TestDiagnostics(t *testing.T) {
	// Syntactically invalid input
	invalidInput := `1: 
2: {
`
	diags := Diagnostics("invalid.protoscope", []byte(invalidInput))
	assert.NotEmpty(t, diags)

	var hasError bool
	for _, diag := range diags {
		if diag.Level == SeverityError {
			hasError = true
		}
		assert.NotEmpty(t, diag.Message)
		assert.Positive(t, diag.Range.Start.Line)
	}
	assert.True(t, hasError)
}

func TestDocumentSymbols(t *testing.T) {
	input := `1: 150
2: {
  3: "hello"
}
`
	symbols, diags := DocumentSymbols("test.protoscope", []byte(input))
	require.Empty(t, diags)
	require.Len(t, symbols, 2)

	// First symbol: Field 1
	assert.Equal(t, "1:", symbols[0].Name)
	assert.Equal(t, "field", symbols[0].Kind)

	// Second symbol: Field 2 containing Block
	assert.Equal(t, "2:", symbols[1].Name)
	assert.Equal(t, "field", symbols[1].Kind)
	require.Len(t, symbols[1].Children, 1)

	// Block symbol
	block := symbols[1].Children[0]
	assert.Equal(t, "Length-Prefixed", block.Name)
	assert.Equal(t, "block", block.Kind)
	require.Len(t, block.Children, 1)

	// Inside Block: Field 3
	field3 := block.Children[0]
	assert.Equal(t, "3:", field3.Name)
	assert.Equal(t, "field", field3.Kind)
}

func TestHover(t *testing.T) {
	input := `1: 150
2: {
  3: ` + "`" + `01 02 03` + "`" + `
}
`
	// Test hover over "1:" (line 1, column 1)
	h1, err := Hover("test.protoscope", []byte(input), 1, 1)
	require.NoError(t, err)
	require.NotNil(t, h1)
	assert.Contains(t, h1.Text, "Field Tag")
	assert.Contains(t, h1.Text, "**Field Number:** `1`")

	// Test hover over "150" (line 1, column 4)
	h2, err := Hover("test.protoscope", []byte(input), 1, 4)
	require.NoError(t, err)
	require.NotNil(t, h2)
	assert.Contains(t, h2.Text, "Literal Value")
	assert.Contains(t, h2.Text, "**Raw Text:** `150`")
	assert.Contains(t, h2.Text, "**Decimal:** `150`")
	assert.Contains(t, h2.Text, "**Hexadecimal:** `0x96`")

	// Test hover over Hex string literal (line 3, column 6)
	h3, err := Hover("test.protoscope", []byte(input), 3, 6)
	require.NoError(t, err)
	require.NotNil(t, h3)
	assert.Contains(t, h3.Text, "Literal Value")
	assert.Contains(t, h3.Text, "**Raw Hex:** ``01 02 03``")
	assert.Contains(t, h3.Text, "**Type:** `String`")
	assert.Contains(t, h3.Text, "**Hex Length:** `3 bytes`")

	// Test hover over Hex string literal with UTF-8
	utf8Input := "1: {`e6 97 a5 e6 9c ac e8 aa 9e`}\n"
	h4, err := Hover("utf8.protoscope", []byte(utf8Input), 1, 5)
	require.NoError(t, err)
	require.NotNil(t, h4)
	assert.Contains(t, h4.Text, "**Decoded Text:** `日本語`", "Should decode UTF-8 text")

	// Test hover over standard string literal with multi-byte runes
	stdStringInput := `1: "Hello, UTF-8 text! 日本語, 𐍈, 💻, 🚀"`
	h5, err := Hover("std.protoscope", []byte(stdStringInput), 1, 5)
	require.NoError(t, err)
	require.NotNil(t, h5)
	assert.Contains(t, h5.Text, "**Length:** `46 bytes` (`31 characters`)", "Should output correct byte/character length")
}

func TestPossibilities(t *testing.T) {
	// wireVarint = 0, payload = [0x96, 0x01] (varint for 150)
	reps := Possibilities(0, []byte{0x96, 0x01})
	require.NotEmpty(t, reps)

	var foundVarint bool
	for _, r := range reps {
		if r.Type == "varint" {
			foundVarint = true
			assert.Equal(t, "150", r.Text)
			assert.Equal(t, "Varint", r.Description)
		}
	}
	assert.True(t, foundVarint, "Should have found varint representation")
}


func TestMultiFrameAndVariants(t *testing.T) {
	// 1. Raw variant with single frame
	rawInput := `1: 150
2: "hello"
`
	binary, diags := AssembleWithOptions("raw.protoscope", []byte(rawInput), AssembleOptions{Variant: "raw"})
	require.Empty(t, diags)
	// Output should be concatenated binary:
	// 1: 150 -> 08 96 01
	// 2: "hello" -> 12 05 68 65 6c 6c 6f
	expectedRaw := []byte{0x08, 0x96, 0x01, 0x12, 0x05, 0x68, 0x65, 0x6c, 0x6c, 0x6f}
	assert.Equal(t, expectedRaw, binary)


	// Since raw has no headers, disassemble raw treats the whole stream as 1 message.
	disText, err := Disassemble(binary, DisassembleOptions{Variant: "raw"})
	require.NoError(t, err)
	assert.Contains(t, disText, "1: 150")
	assert.Contains(t, disText, `2: {"hello"}`) // disassembled as tag 2 since it was concatenated

	// 2. Varint delimited variant with multiple frames
	varintInput := `1: 150
---
2: "hello"
`
	binaryV, diagsV := AssembleWithOptions("varint.protoscope", []byte(varintInput), AssembleOptions{Variant: "varint"})
	require.Empty(t, diagsV)
	// Frame 1: len 3, Frame 2: len 7
	expectedV := []byte{
		3, 0x08, 0x96, 0x01,
		7, 0x12, 0x05, 0x68, 0x65, 0x6c, 0x6c, 0x6f,
	}
	assert.Equal(t, expectedV, binaryV)

	disTextV, err := Disassemble(binaryV, DisassembleOptions{Variant: "varint"})
	require.NoError(t, err)
	assert.Equal(t, "1: 150\n---\n2: {\"hello\"}\n", disTextV)

	// 3. gRPC / ConnectRPC variant with custom flags
	grpcInput := `# flags: 1
1: 150
---
# flag: 2
2: "hello"
`
	binaryG, diagsG := AssembleWithOptions("grpc.protoscope", []byte(grpcInput), AssembleOptions{Variant: "grpc"})
	require.Empty(t, diagsG)
	// Frame 1: flags 1, len 3 -> 01, 00 00 00 03, 08 96 01
	// Frame 2: flags 2, len 7 -> 02, 00 00 00 07, 12 05 68 65 6c 6c 6f
	expectedG := []byte{
		1, 0x00, 0x00, 0x00, 0x03, 0x08, 0x96, 0x01,
		2, 0x00, 0x00, 0x00, 0x07, 0x12, 0x05, 0x68, 0x65, 0x6c, 0x6c, 0x6f,
	}
	assert.Equal(t, expectedG, binaryG)

	disTextG, err := Disassemble(binaryG, DisassembleOptions{Variant: "grpc"})
	require.NoError(t, err)
	assert.Equal(t, "# flags: 1\n1: 150\n---\n# flags: 2\n2: {\"hello\"}\n", disTextG)

	// 4. Test diagnostics shifting across multiple frames
	invalidInput := `1: 150
---
2: {
# syntax error in second frame
`
	diagsErr := Diagnostics("test.protoscope", []byte(invalidInput))
	require.NotEmpty(t, diagsErr)
	// The error should be in the second frame (after line 2)
	assert.Greater(t, diagsErr[0].Range.Start.Line, 2)

	// 5. Test DocumentSymbols and Hover on multi-frame inputs
	symbolInput := `1: 150
---
2: 30
`
	symbols, diagsSym := DocumentSymbols("symbols.protoscope", []byte(symbolInput))
	require.Empty(t, diagsSym)
	require.Len(t, symbols, 2)
	// Symbol 1 starts on line 1
	assert.Equal(t, 1, symbols[0].Range.Start.Line)
	// Symbol 2 starts on line 3 (after ---)
	assert.Equal(t, 3, symbols[1].Range.Start.Line)

	// Hover test
	hover1, err := Hover("symbols.protoscope", []byte(symbolInput), 1, 1)
	require.NoError(t, err)
	require.NotNil(t, hover1)
	assert.Equal(t, 1, hover1.Range.Start.Line)

	hover2, err := Hover("symbols.protoscope", []byte(symbolInput), 3, 1)
	require.NoError(t, err)
	require.NotNil(t, hover2)
	assert.Equal(t, 3, hover2.Range.Start.Line)

	// 6. Test multi-frame raw variant error
	multiRawInput := "1: 150\n---\n2: \"hello\"\n"
	_, rawDiags := AssembleWithOptions("raw.protoscope", []byte(multiRawInput), AssembleOptions{Variant: "raw"})
	require.NotEmpty(t, rawDiags)
	assert.Equal(t, "multiple frames are not supported for raw variant", rawDiags[0].Message)
	assert.Equal(t, 2, rawDiags[0].Range.Start.Line)
}

func TestAllVariantsRoundtrip(t *testing.T) {
	t.Parallel()

	input := `1: 150
---
2: {"hello"}
`

	variants := []string{
		"grpc",
		"connect",
		"connectrpc",
		"varint",
		"varint delimited",
	}

	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			// Assemble
			binary, diags := AssembleWithOptions("test.protoscope", []byte(input), AssembleOptions{Variant: variant})
			require.Empty(t, diags)
			require.NotEmpty(t, binary)

			// Disassemble
			disassembled, err := Disassemble(binary, DisassembleOptions{Variant: variant})
			require.NoError(t, err)

			// The output should contain our fields and be properly split by ---
			assert.Contains(t, disassembled, "1: 150")
			assert.Contains(t, disassembled, "---")
			assert.Contains(t, disassembled, `2: {"hello"}`)
		})
	}
}

func TestDisassembleFallback(t *testing.T) {
	t.Parallel()

	// gRPC message `1: 55` has bytes:
	// 00 00 00 00 02 08 37
	grpcBytes := []byte{0x00, 0x00, 0x00, 0x00, 0x02, 0x08, 0x37}

	// Disassembling without variant should trigger invalid tag 0 error fallback comment
	disassembled, err := Disassemble(grpcBytes, DisassembleOptions{})
	require.NoError(t, err)
	assert.Contains(t, disassembled, "# Error: invalid tag 0; this might be using a different framing variant (e.g. gRPC)")
	assert.Contains(t, disassembled, "`00 00 00 00 02 08 37`")
}
