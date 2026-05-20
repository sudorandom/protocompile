package lexer

import (
	"testing"
	"github.com/bufbuild/protocompile/experimental/report"
	"github.com/bufbuild/protocompile/experimental/source"
	"github.com/bufbuild/protocompile/experimental/token"
)

func TestReproduceFuzzCrash(t *testing.T) {
	input := "2E50000000"
	src := source.NewFile("fuzz.protoscope", input)
	
	r := &report.Report{}
	l := &lexer{
		Lexer: &Lexer{},
		Stream: &token.Stream{File: src},
		Report: r,
		cursor: 0,
	}
	lexNumber(l)
}
