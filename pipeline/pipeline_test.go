package pipeline_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/pipeline"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

func recording(name string, log *[]string) pipeline.Middleware {
	return pipeline.Func(name, func(ctx *invocation.Context, next pipeline.Step) primitives.Result[primitives.Unit] {
		*log = append(*log, name+":before")
		outcome := next(ctx)
		*log = append(*log, name+":after")
		return outcome
	})
}

func TestOrderFollowsStagePlacementOwnerAndRegistration(t *testing.T) {
	var log []string
	builder := pipeline.NewBuilder().
		UseAt(recording("Egress", &log), primitives.Egress, pipeline.End, "app").
		UseAt(recording("ObserveLate", &log), primitives.Observe, pipeline.End, "xihan.b").
		UseAt(recording("ObserveEarly", &log), primitives.Observe, pipeline.Beginning, "xihan.z").
		UseAt(recording("ObserveA", &log), primitives.Observe, pipeline.End, "xihan.a").
		Use(recording("Transaction", &log))
	dump, err := builder.Dump()
	if err != nil {
		t.Fatal(err)
	}
	want := "observe      ObserveEarly\nobserve      ObserveA\nobserve      ObserveLate\ntransaction  Transaction\negress       Egress\n"
	if dump != want {
		t.Errorf("dump:\n%s", dump)
	}
	step, err := builder.Build(func(*invocation.Context) primitives.Result[primitives.Unit] {
		log = append(log, "terminal")
		return primitives.Success()
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome := step(invocation.Background()); !outcome.OK() {
		t.Fatal(outcome)
	}
	if strings.Join(log, ",") != "ObserveEarly:before,ObserveA:before,ObserveLate:before,Transaction:before,Egress:before,terminal,Egress:after,Transaction:after,ObserveLate:after,ObserveA:after,ObserveEarly:after" {
		t.Errorf("log %v", log)
	}
}

func TestConstraintsReorderWithinAStage(t *testing.T) {
	var log []string
	builder := pipeline.NewBuilder().
		UseAt(recording("A", &log), primitives.Validate, pipeline.End, "app").
		UseAt(recording("B", &log), primitives.Validate, pipeline.End, "app").
		UseAt(recording("C", &log), primitives.Validate, pipeline.End, "app").
		Before("C", "A").
		After("B", "C")
	descriptors, err := builder.Describe()
	if err != nil {
		t.Fatal(err)
	}
	if descriptors[0].Middleware != "C" || descriptors[1].Middleware != "A" || descriptors[2].Middleware != "B" {
		t.Errorf("order %v", descriptors)
	}
}

func TestCyclesAndBadConstraintsAreRejected(t *testing.T) {
	cycle := pipeline.NewBuilder().
		UseAt(pipeline.Func("A", nil), primitives.Validate, pipeline.End, "app").
		UseAt(pipeline.Func("B", nil), primitives.Validate, pipeline.End, "app").
		Before("A", "B").
		Before("B", "A")
	assertCode(t, cycle, pipeline.ErrorCodeCycle)

	unregistered := pipeline.NewBuilder().UseAt(pipeline.Func("A", nil), primitives.Validate, pipeline.End, "app").Before("A", "Missing")
	assertCode(t, unregistered, pipeline.ErrorCodeRegistration)

	crossStage := pipeline.NewBuilder().
		UseAt(pipeline.Func("A", nil), primitives.Validate, pipeline.End, "app").
		UseAt(pipeline.Func("B", nil), primitives.Egress, pipeline.End, "app").
		Before("A", "B")
	assertCode(t, crossStage, pipeline.ErrorCodeRegistration)

	duplicate := pipeline.NewBuilder().UseAt(pipeline.Func("A", nil), primitives.Validate, pipeline.End, "app").UseAt(pipeline.Func("A", nil), primitives.Validate, pipeline.End, "app")
	assertCode(t, duplicate, pipeline.ErrorCodeRegistration)

	if _, err := pipeline.ParseStage("middle"); err == nil {
		t.Error("unknown stage")
	} else if code := errorCode(err); code != pipeline.ErrorCodeUnknownStage {
		t.Errorf("code %s", code)
	}
}

func TestNamedInstancesAreDistinctButAmbiguousForTypeConstraints(t *testing.T) {
	var log []string
	builder := pipeline.NewBuilder().
		UseNamedInstance("guard-a", recording("Guard", &log), primitives.Observe, pipeline.End, "xihan.plugins.a").
		UseNamedInstance("guard-b", recording("Guard", &log), primitives.Observe, pipeline.End, "xihan.plugins.b")
	dump, err := builder.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if dump != "observe      Guard[guard-a]\nobserve      Guard[guard-b]\n" {
		t.Errorf("dump:\n%s", dump)
	}
	assertCode(t, builder.UseAt(pipeline.Func("Other", nil), primitives.Observe, pipeline.End, "app").Before("Guard", "Other"), pipeline.ErrorCodeRegistration)
}

func TestShortCircuitSkipsDownstream(t *testing.T) {
	var log []string
	denied := primitives.Denied("ORDERS_DENIED_001", "orders.denied", nil)
	builder := pipeline.NewBuilder().
		UseAt(pipeline.Func("Deny", func(*invocation.Context, pipeline.Step) primitives.Result[primitives.Unit] {
			return primitives.Fail[primitives.Unit](denied)
		}), primitives.Authorize, pipeline.End, "app").
		UseAt(recording("Handle", &log), primitives.Handle, pipeline.End, "app")
	step, err := builder.Build(func(*invocation.Context) primitives.Result[primitives.Unit] { return primitives.Success() })
	if err != nil {
		t.Fatal(err)
	}
	outcome := step(invocation.Background())
	if outcome.OK() || outcome.Err().Code != "ORDERS_DENIED_001" || len(log) != 0 {
		t.Errorf("outcome %v log %v", outcome, log)
	}
}

func assertCode(t *testing.T, builder *pipeline.Builder, code string) {
	t.Helper()
	_, err := builder.Describe()
	if err == nil {
		t.Errorf("expected %s", code)
		return
	}
	if got := errorCode(err); got != code {
		t.Errorf("code %s, want %s (%v)", got, code, err)
	}
}

func errorCode(err error) string {
	var framework *primitives.FrameworkError
	if errors.As(err, &framework) {
		return framework.Code
	}
	return ""
}
