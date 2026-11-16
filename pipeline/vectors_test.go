package pipeline_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// vectorType 是回放 ordering.json 时按名字登记的实例用的类型名，替换 dump 里的 {type}
const vectorType = "Middleware"

// step 是 ordering.json 的一步：use 按名字登记一个实例，before / after 是 [主体, 目标] 的按名字约束
type step struct {
	Use       string    `json:"use"`
	Stage     string    `json:"stage"`
	Placement string    `json:"placement"`
	Owner     string    `json:"owner"`
	Before    [2]string `json:"before"`
	After     [2]string `json:"after"`
}

// apply 执行一步；登记时立即报的错误（panic 的 FrameworkError，或阶段名不合法）作为返回值
func apply(t *testing.T, builder *pipeline.Builder, raw json.RawMessage) (err error) {
	t.Helper()
	var s step
	if decodeErr := json.Unmarshal(raw, &s); decodeErr != nil {
		t.Fatalf("step %s: %v", raw, decodeErr)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			framework, ok := recovered.(*primitives.FrameworkError)
			if !ok {
				panic(recovered)
			}
			err = framework
		}
	}()
	switch {
	case s.Use != "":
		stage, stageErr := pipeline.ParseStage(s.Stage)
		if stageErr != nil {
			return stageErr
		}
		placement := pipeline.End
		if s.Placement == "beginning" {
			placement = pipeline.Beginning
		}
		builder.UseNamedInstance(s.Use, pipeline.Func(vectorType, nil), stage, placement, s.Owner)
	case s.Before[0] != "":
		builder.Before(s.Before[0], s.Before[1])
	case s.After[0] != "":
		builder.After(s.After[0], s.After[1])
	default:
		t.Fatalf("unknown step %s", raw)
	}
	return nil
}

// TestPipelineOrderingFollowsTheVectors 回放 ordering.json：期望的 Dump 全文，以及每种错误的错误码、报出时机与消息原文（RFC-0004，ADR-0235、ADR-0236）
func TestPipelineOrderingFollowsTheVectors(t *testing.T) {
	vectors, err := conformance.PipelineOrdering()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Cases {
		builder := pipeline.NewBuilder()
		for index, raw := range c.Steps {
			if err := apply(t, builder, raw); err != nil {
				t.Fatalf("%s: step %d: %v", c.Name, index, err)
			}
		}
		dump, err := builder.Dump()
		if want := strings.ReplaceAll(c.Dump, "{type}", vectorType); err != nil || dump != want {
			t.Errorf("%s: dump %v\n%s\nwant:\n%s", c.Name, err, dump, want)
		}
	}
	for _, c := range vectors.Errors {
		builder := pipeline.NewBuilder()
		at, failed := "sort", error(nil)
		for index, raw := range c.Steps {
			if failed = apply(t, builder, raw); failed != nil {
				at = strconv.Itoa(index)
				break
			}
		}
		if failed == nil {
			_, failed = builder.Describe()
		}
		var want string
		if err := json.Unmarshal(c.At, &want); err != nil {
			var index int
			if err := json.Unmarshal(c.At, &index); err != nil {
				t.Fatalf("%s: at %s", c.Name, c.At)
			}
			want = strconv.Itoa(index)
		}
		var framework *primitives.FrameworkError
		if !errors.As(failed, &framework) || framework.Code != c.Error || at != want {
			t.Errorf("%s: error %v at %s, want %s at %s", c.Name, failed, at, c.Error, want)
			continue
		}
		// 消息是配置错误的英文原文，逐字相等（RFC-0004 规则 8，ADR-0236）
		if message := strings.ReplaceAll(c.Message, "{type}", vectorType); c.Message == "" || framework.Message != message {
			t.Errorf("%s: message %q, want %q", c.Name, framework.Message, message)
		}
	}
}
