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

	duplicate := pipeline.NewBuilder().UseAt(pipeline.Func("A", nil), primitives.Validate, pipeline.End, "app")
	assertPanicCode(t, pipeline.ErrorCodeRegistration, func() { duplicate.UseAt(pipeline.Func("A", nil), primitives.Validate, pipeline.End, "app") })
	assertPanicCode(t, pipeline.ErrorCodeUnknownStage, func() { duplicate.UseAt(pipeline.Func("B", nil), primitives.PipelineStage(42), pipeline.End, "app") })
	if _, err := duplicate.Describe(); err != nil {
		t.Errorf("a rejected registration leaves the builder as it was: %v", err)
	}

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
	builder.UseAt(pipeline.Func("Other", nil), primitives.Observe, pipeline.End, "app")
	assertPanicCode(t, pipeline.ErrorCodeRegistration, func() { builder.Before("Guard", "Other") })
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

// TestConstraintsByInstanceName 核对 RFC-0004 规则 7：约束两端可以是命名实例的实例名，也可以是只登记了一个实例的类型名
func TestConstraintsByInstanceName(t *testing.T) {
	builder := pipeline.NewBuilder().
		UseNamedInstance("guard-a", pipeline.Func("Guard", nil), primitives.Observe, pipeline.End, "app").
		UseNamedInstance("guard-b", pipeline.Func("Guard", nil), primitives.Observe, pipeline.End, "app").
		UseAt(pipeline.Func("Audit", nil), primitives.Observe, pipeline.End, "app").
		UseNamedInstance("only", pipeline.Func("Single", nil), primitives.Observe, pipeline.End, "app").
		Before("guard-b", "guard-a").
		After("guard-a", "Audit").
		Before("Single", "guard-b")
	dump, err := builder.Dump()
	if err != nil {
		t.Fatal(err)
	}
	// 基准次序 guard-a、guard-b、Audit、Single；稳定拓扑排序每次取基准次序最靠前、没有前驱的那个
	want := "observe      Audit\nobserve      Single[only]\nobserve      Guard[guard-b]\nobserve      Guard[guard-a]\n"
	if dump != want {
		t.Errorf("dump:\n%s\nwant:\n%s", dump, want)
	}
}

// TestConstraintSubjectMustBeRegisteredWhenDeclared 核对约束主体在声明时必须已登记，目标在排序时解析、可以晚于约束登记
func TestConstraintSubjectMustBeRegisteredWhenDeclared(t *testing.T) {
	assertPanicCode(t, pipeline.ErrorCodeRegistration, func() { pipeline.NewBuilder().Before("A", "B") })
	assertPanicCode(t, pipeline.ErrorCodeRegistration, func() {
		pipeline.NewBuilder().UseAt(pipeline.Func("B", nil), primitives.Validate, pipeline.End, "app").After("A", "B")
	})

	targetLater := pipeline.NewBuilder().
		UseAt(pipeline.Func("B", nil), primitives.Validate, pipeline.End, "app").
		UseAt(pipeline.Func("A", nil), primitives.Validate, pipeline.End, "app").
		After("B", "C").
		UseAt(pipeline.Func("C", nil), primitives.Validate, pipeline.End, "app")
	descriptors, err := targetLater.Describe()
	if err != nil {
		t.Fatal(err)
	}
	if descriptors[0].Middleware != "A" || descriptors[1].Middleware != "C" || descriptors[2].Middleware != "B" {
		t.Errorf("order %v", descriptors)
	}
}

// TestAmbiguousNamesAreRejected 核对名字命中多个登记即 KERNEL_PIPELINE_003：同类型的多个实例按类型名引用，或实例名与别的类型名相同
func TestAmbiguousNamesAreRejected(t *testing.T) {
	two := func() *pipeline.Builder {
		return pipeline.NewBuilder().
			UseNamedInstance("guard-a", pipeline.Func("Guard", nil), primitives.Observe, pipeline.End, "app").
			UseNamedInstance("guard-b", pipeline.Func("Guard", nil), primitives.Observe, pipeline.End, "app").
			UseAt(pipeline.Func("Audit", nil), primitives.Observe, pipeline.End, "app")
	}
	// 主体有歧义在声明时报，目标有歧义在排序时报
	assertPanicCode(t, pipeline.ErrorCodeRegistration, func() { two().Before("Guard", "Audit") })
	assertCode(t, two().Before("Audit", "Guard"), pipeline.ErrorCodeRegistration)

	collision := pipeline.NewBuilder().
		UseNamedInstance("Audit", pipeline.Func("Guard", nil), primitives.Observe, pipeline.End, "app").
		UseAt(pipeline.Func("Audit", nil), primitives.Observe, pipeline.End, "app").
		UseAt(pipeline.Func("Trace", nil), primitives.Observe, pipeline.End, "app")
	assertCode(t, collision.Before("Trace", "Audit"), pipeline.ErrorCodeRegistration)

	duplicateInstance := pipeline.NewBuilder().
		UseNamedInstance("guard-a", pipeline.Func("Guard", nil), primitives.Observe, pipeline.End, "app")
	assertPanicCode(t, pipeline.ErrorCodeRegistration, func() {
		duplicateInstance.UseNamedInstance("guard-a", pipeline.Func("Guard", nil), primitives.Observe, pipeline.End, "xihan.other")
	})
}

// assertPanicCode 核对 fn 以给定错误码的 FrameworkError panic：登记时立即报的错误（RFC-0004 规则 8）
func assertPanicCode(t *testing.T, code string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		recovered := recover()
		framework, ok := recovered.(*primitives.FrameworkError)
		if !ok || framework.Code != code {
			t.Errorf("expected a panic with %s, got %v", code, recovered)
		}
	}()
	fn()
}

// panicMessage 返回 fn panic 的 FrameworkError 的消息
func panicMessage(t *testing.T, fn func()) (message string) {
	t.Helper()
	defer func() {
		framework, ok := recover().(*primitives.FrameworkError)
		if !ok {
			t.Fatal("expected a FrameworkError panic")
		}
		message = framework.Message
	}()
	fn()
	return ""
}

// sortMessage 返回排序时报的 FrameworkError 的消息
func sortMessage(t *testing.T, builder *pipeline.Builder) string {
	t.Helper()
	_, err := builder.Describe()
	var framework *primitives.FrameworkError
	if !errors.As(err, &framework) {
		t.Fatalf("expected a FrameworkError, got %v", err)
	}
	return framework.Message
}

// TestErrorMessagesAreTheReferenceText 核对配置错误的消息与 .NET 的英文原文逐字相同（RFC-0004 规则 8，ADR-0236）：按名字登记的实例
// 写成 类型名[名字]，成环时按基准次序列出全部仍被卡住的中间件
func TestErrorMessagesAreTheReferenceText(t *testing.T) {
	named := func(name string, stage primitives.PipelineStage) func(*pipeline.Builder) *pipeline.Builder {
		return func(builder *pipeline.Builder) *pipeline.Builder {
			return builder.UseNamedInstance(name, pipeline.Func("Guard", nil), stage, pipeline.End, "app")
		}
	}
	build := func(steps ...func(*pipeline.Builder) *pipeline.Builder) *pipeline.Builder {
		builder := pipeline.NewBuilder()
		for _, step := range steps {
			builder = step(builder)
		}
		return builder
	}
	cases := []struct{ got, want string }{
		{sortMessage(t, build(named("a", primitives.Validate), named("b", primitives.Validate), named("c", primitives.Validate)).Before("a", "b").Before("b", "a")),
			"Middleware ordering constraints in stage 'validate' form a cycle: 'Guard[a]', 'Guard[b]'."},
		{sortMessage(t, build(named("a", primitives.Validate), named("b", primitives.Validate)).Before("a", "zz")),
			"The ordering constraint of middleware 'Guard[a]' references unregistered middleware 'zz'."},
		{sortMessage(t, build(named("a", primitives.Validate), named("b", primitives.Handle)).Before("a", "b")),
			"Middleware 'Guard[a]' (stage 'validate') and 'b' (stage 'handle') are not in the same stage, so no relative order can be declared between them."},
		{sortMessage(t, build(named("a", primitives.Validate), named("b", primitives.Validate)).Before("a", "Guard")),
			"The middleware name 'Guard' matches several registrations, so ordering constraints by name are ambiguous for it."},
		{panicMessage(t, func() { build(named("a", primitives.Validate), named("a", primitives.Validate)) }),
			"Middleware 'Guard' named 'a' is already registered; instance names must be unique within a type."},
		{panicMessage(t, func() {
			pipeline.NewBuilder().Use(pipeline.Func("Audit", nil)).Use(pipeline.Func("Audit", nil))
		}), "Middleware 'Audit' is already registered; each middleware type may appear only once in a pipeline unless its instances are named."},
		{panicMessage(t, func() { pipeline.NewBuilder().Before("q", "a") }),
			"Middleware 'q' is not registered, so no ordering constraint can be declared for it."},
		{panicMessage(t, func() {
			pipeline.NewBuilder().UseAt(pipeline.Func("Audit", nil), primitives.PipelineStage(42), pipeline.End, "app")
		}),
			"Unknown pipeline stage '42'."},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("message %q\nwant    %q", c.got, c.want)
		}
	}
	if _, err := pipeline.ParseStage("bogus"); err == nil || err.(*primitives.FrameworkError).Message != "Unknown pipeline stage 'bogus'." {
		t.Errorf("ParseStage %v", err)
	}
	// 前一阶段成环先于后一阶段的约束目标未登记：阶段逐个处理
	mixed := build(named("a", primitives.Observe), named("b", primitives.Observe), named("c", primitives.Handle)).Before("a", "b").Before("b", "a").Before("c", "zz")
	if got := sortMessage(t, mixed); !strings.HasPrefix(got, "Middleware ordering constraints in stage 'observe' form a cycle") {
		t.Errorf("an earlier stage is checked first: %s", got)
	}
}
