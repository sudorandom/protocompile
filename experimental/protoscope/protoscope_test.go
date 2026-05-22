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
