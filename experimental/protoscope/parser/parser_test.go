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

package parser

import (
	"testing"

	"github.com/bufbuild/protocompile/experimental/report"
	"github.com/bufbuild/protocompile/experimental/source"
)

func TestParse(t *testing.T) {
	input := "1: 150"
	src := source.NewFile("test.protoscope", input)
	r := &report.Report{}
	file, ok := Parse("test.protoscope", src, r)
	if !ok {
		t.Fatalf("Parse failed: %v", r.Diagnostics)
	}

	if file.Decls().Len() == 0 {
		t.Errorf("expected at least one declaration, got 0")
		t.Logf("token stream: %v", file.Stream())
		c := file.Stream().Cursor()
		for !c.Done() {
			t.Logf("token: %v (%q)", c.Peek(), c.Peek().Text())
			_ = c.Next()
		}
	}
}
