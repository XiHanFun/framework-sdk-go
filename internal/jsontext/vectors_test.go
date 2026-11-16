package jsontext_test

import (
	"testing"

	"github.com/xihanfun/framework-spec/conformance"
	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/internal/jsontext"
)

// TestCheckFollowsTheJSONTextVectors 回放 json-text.json（RFC-0001「JSON 文本」，ADR-0236）：valid 通过，syntax 不是 JSON、位置只写 $，
// duplicate 报第一处重复的路径；结果与一致性模块的独立实现 rules.CheckJSONText 相同
func TestCheckFollowsTheJSONTextVectors(t *testing.T) {
	vectors, err := conformance.JSONText()
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range vectors.Cases {
		data, err := c.Bytes()
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		problem := jsontext.Check(data)
		switch c.Expect {
		case "valid":
			if problem != nil {
				t.Errorf("%s: must be valid, got %v", c.Name, problem)
			}
		case "syntax":
			if problem == nil || !problem.Syntax || problem.Path != "$" {
				t.Errorf("%s: must be a syntax error at $, got %+v", c.Name, problem)
			}
		case "duplicate":
			if problem == nil || problem.Syntax || problem.Path != c.Path {
				t.Errorf("%s: must be a duplicate at %s, got %+v", c.Name, c.Path, problem)
			}
		default:
			t.Fatalf("%s: unknown expectation %q", c.Name, c.Expect)
		}
		reference := rules.CheckJSONText(data)
		if (problem == nil) != (reference == nil) || (problem != nil && (problem.Syntax != reference.Syntax || problem.Path != reference.Path)) {
			t.Errorf("%s: %+v, rules.CheckJSONText %+v", c.Name, problem, reference)
		}
	}
}
