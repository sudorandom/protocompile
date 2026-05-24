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

package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bufbuild/protocompile/experimental/internal/protoscope/assembler"
	"github.com/bufbuild/protocompile/experimental/internal/protoscope/disassembler"
	"github.com/bufbuild/protocompile/experimental/internal/protoscope/parser"
	"github.com/bufbuild/protocompile/experimental/report"
	"github.com/bufbuild/protocompile/experimental/source"
)

func main() {
	disassembleFlag := flag.Bool("d", false, "disassemble binary input to protoscope text")
	explicitWireTypes := flag.Bool("explicit-wire-types", false, "emit explicit wire types in disassembly")
	explicitLengthPrefixes := flag.Bool("explicit-length-prefixes", false, "emit explicit length prefixes in disassembly")
	noGroups := flag.Bool("no-groups", false, "disable group output in disassembly")
	var variant string
	flag.StringVar(&variant, "variant", "raw", "protobuf message variant (raw, grpc, connectrpc, varint)")
	flag.StringVar(&variant, "v", "raw", "protobuf message variant (raw, grpc, connectrpc, varint) (shorthand)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pbscope [-d] [-explicit-wire-types] [-explicit-length-prefixes] [-no-groups] [-variant <variant>] <file>")
		os.Exit(1)
	}

	path := flag.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *disassembleFlag {
		opts := disassembler.Options{
			ExplicitWireTypes:      *explicitWireTypes,
			ExplicitLengthPrefixes: *explicitLengthPrefixes,
			NoGroups:               *noGroups,
			Variant:                variant,
		}
		if err := disassembler.DisassembleWithOptions(data, os.Stdout, opts); err != nil {
			fmt.Fprintf(os.Stderr, "disassembly error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	frames := splitFrames(data)
	parentFile := source.NewFile(path, string(data))
	r := &report.Report{}

	variantNormalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(variant, " ", ""), "-", ""))
	if len(frames) > 1 && (variantNormalized == "raw" || variantNormalized == "") {
		sepStart := strings.LastIndex(parentFile.Text()[:frames[1].byteOffset], "---")
		if sepStart != -1 {
			r.Errorf("multiple frames are not supported for raw variant").Apply(
				report.Snippet(parentFile.Span(sepStart, sepStart+3)),
			)
		} else {
			r.Errorf("multiple frames are not supported for raw variant")
		}
	}

	var payloads [][]byte
	var flags []byte
	hasError := false

	for _, frame := range frames {
		var frameFlags byte
		frameLines := strings.Split(frame.text, "\n")
		for _, line := range frameLines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "#") {
				comment := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
				if strings.HasPrefix(comment, "flags:") {
					valStr := strings.TrimSpace(strings.TrimPrefix(comment, "flags:"))
					if val, err := strconv.ParseUint(valStr, 10, 8); err == nil {
						frameFlags = byte(val)
					}
				} else if strings.HasPrefix(comment, "flag:") {
					valStr := strings.TrimSpace(strings.TrimPrefix(comment, "flag:"))
					if val, err := strconv.ParseUint(valStr, 10, 8); err == nil {
						frameFlags = byte(val)
					}
				}
			} else {
				break
			}
		}
		flags = append(flags, frameFlags)

		src := source.NewFile(path, frame.text)
		frameReport := &report.Report{}
		file, ok := parser.Parse(path, src, frameReport)

		report.ShiftReportSpans(frameReport, parentFile, frame.byteOffset)
		r.Diagnostics = append(r.Diagnostics, frameReport.Diagnostics...)

		if !ok || file == nil {
			hasError = true
			continue
		}

		out := assembler.Assemble(file)
		payloads = append(payloads, out)
	}

	if hasError || len(r.Diagnostics) > 0 {
		renderer := report.Renderer{Colorize: true}
		_, _, _ = renderer.Render(r, os.Stderr)

		hasActualError := false
		for _, diag := range r.Diagnostics {
			if diag.Level() == report.Error || diag.Level() == report.ICE {
				hasActualError = true
				break
			}
		}
		if hasActualError {
			os.Exit(1)
		}
	}

	var result []byte
	switch variantNormalized {
	case "grpc", "connectrpc", "connect":
		for i, payload := range payloads {
			header := make([]byte, 5)
			header[0] = flags[i]
			binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))
			result = append(result, header...)
			result = append(result, payload...)
		}
	case "varint", "varintdelimited":
		for _, payload := range payloads {
			var lengthBuf [10]byte
			n := binary.PutUvarint(lengthBuf[:], uint64(len(payload)))
			result = append(result, lengthBuf[:n]...)
			result = append(result, payload...)
		}
	default:
		for _, payload := range payloads {
			result = append(result, payload...)
		}
	}

	_, _ = os.Stdout.Write(result)
}

type frameInfo struct {
	text       string
	byteOffset int
	lineOffset int
}

func splitFrames(text []byte) []frameInfo {
	var frames []frameInfo
	s := string(text)
	lines := strings.Split(s, "\n")
	var currentFrame strings.Builder
	frameLineOffset := 0
	frameByteOffset := 0
	currentByteOffset := 0

	for i, line := range lines {
		lineLen := len(line)
		if i < len(lines)-1 {
			lineLen += 1
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			frames = append(frames, frameInfo{
				text:       currentFrame.String(),
				byteOffset: frameByteOffset,
				lineOffset: frameLineOffset,
			})
			currentFrame.Reset()
			frameLineOffset = i + 1
			frameByteOffset = currentByteOffset + lineLen
		} else {
			currentFrame.WriteString(line)
			if i < len(lines)-1 {
				currentFrame.WriteByte('\n')
			}
		}
		currentByteOffset += lineLen
	}
	frames = append(frames, frameInfo{
		text:       currentFrame.String(),
		byteOffset: frameByteOffset,
		lineOffset: frameLineOffset,
	})
	return frames
}
