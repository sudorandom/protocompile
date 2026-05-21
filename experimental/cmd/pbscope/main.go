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
	"flag"
	"fmt"
	"os"

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
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pbscope [-d] [-explicit-wire-types] [-explicit-length-prefixes] [-no-groups] <file>")
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
		}
		if err := disassembler.DisassembleWithOptions(data, os.Stdout, opts); err != nil {
			fmt.Fprintf(os.Stderr, "disassembly error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	src := source.NewFile(path, string(data))
	r := &report.Report{}
	file, ok := parser.Parse(path, src, r)
	if !ok {
		renderer := report.Renderer{Colorize: true}
		_, _, _ = renderer.Render(r, os.Stderr)
		os.Exit(1)
	}

	out := assembler.Assemble(file)
	_, _ = os.Stdout.Write(out)
}
