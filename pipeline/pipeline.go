// Package pipeline 是固定阶段的执行管道（RFC-0004）：中间件按阶段贡献，排序确定、可打印、可测试。
package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// 内核登记的管道错误码。
const (
	// ErrorCodeCycle：中间件顺序约束成环。
	ErrorCodeCycle = "KERNEL_PIPELINE_001"
	// ErrorCodeUnknownStage：阶段名未知。
	ErrorCodeUnknownStage = "KERNEL_PIPELINE_002"
	// ErrorCodeRegistration：中间件重复注册，或顺序约束引用了未注册、跨阶段或有多个实例的中间件。
	ErrorCodeRegistration = "KERNEL_PIPELINE_003"
)

// AppOwner 是应用自己注册的中间件的归属。
const AppOwner = "app"

// KernelOwner 是内核内置中间件的归属（xihan.kernel.core 的清单 ID）：operation.Invoker 的三个内置步骤按它放在所在阶段的 Beginning（RFC-0011 规则 10）
const KernelOwner = "xihan.kernel.core"

// Step 是管道里的一步：中间件调用它进入下游。
type Step func(ctx *invocation.Context) primitives.Result[primitives.Unit]

// Middleware 是一个中间件：Name 是类型名（排序约束与打印用），Invoke 可以在进入下游前后做事、也可以不调 next 而短路。
type Middleware interface {
	Name() string
	Invoke(ctx *invocation.Context, next Step) primitives.Result[primitives.Unit]
}

// funcMiddleware 让函数成为中间件。
type funcMiddleware struct {
	name string
	fn   func(ctx *invocation.Context, next Step) primitives.Result[primitives.Unit]
}

// Func 用名字与函数创建中间件。
func Func(name string, fn func(ctx *invocation.Context, next Step) primitives.Result[primitives.Unit]) Middleware {
	return &funcMiddleware{name: name, fn: fn}
}

// Name 实现 Middleware。
func (m *funcMiddleware) Name() string { return m.name }

// Invoke 实现 Middleware。
func (m *funcMiddleware) Invoke(ctx *invocation.Context, next Step) primitives.Result[primitives.Unit] {
	return m.fn(ctx, next)
}

// Placement 是中间件在阶段内的位置。
type Placement int

// 阶段内位置：Beginning 在前。
const (
	Beginning Placement = iota
	End
)

// String 返回位置名。
func (p Placement) String() string {
	if p == Beginning {
		return "Beginning"
	}
	return "End"
}

// Descriptor 描述一个已排序的中间件。
type Descriptor struct {
	// Stage 是所在阶段。
	Stage primitives.PipelineStage
	// Middleware 是类型名。
	Middleware string
	// Instance 是按名登记的实例名；默认实例为空。
	Instance string
	// Owner 是归属（清单 ID 或 app）。
	Owner string
	// Placement 是阶段内位置。
	Placement Placement
}

// Display 返回打印名：类型名，命名实例为 类型名[实例名]。
func (d Descriptor) Display() string {
	if d.Instance == "" {
		return d.Middleware
	}
	return d.Middleware + "[" + d.Instance + "]"
}

// registration 是一次登记。
type registration struct {
	middleware Middleware
	descriptor Descriptor
	order      int
}

// constraint 是一条顺序约束：主体在声明时已解析成登记，目标是名字、在排序时解析；before 为真时主体在目标之前
type constraint struct {
	subject *registration
	target  string
	before  bool
}

// Builder 收集中间件与顺序约束，按规范排序后构建管道。报错的时机照 RFC-0004 规则 8（ADR-0235）：重复登记、未知阶段、
// 约束主体未登记或按名字有歧义在登记时立即报，是编程错误，panic FrameworkError（与 http.ServeMux 登记冲突时一样）；
// 约束目标未登记、有歧义、跨阶段与成环在排序时报，由 Describe、Dump 与 Build 返回
type Builder struct {
	registrations []*registration
	constraints   []constraint
}

// NewBuilder 创建空的构建器。
func NewBuilder() *Builder { return &Builder{} }

// Clone 复制构建器：登记与约束各自独立，往副本里登记不影响原来的构建器（operation.NewInvoker 在副本里加内置步骤）
func (b *Builder) Clone() *Builder {
	clone := &Builder{registrations: make([]*registration, 0, len(b.registrations)), constraints: make([]constraint, 0, len(b.constraints))}
	copies := make(map[*registration]*registration, len(b.registrations))
	for _, original := range b.registrations {
		duplicate := *original
		copies[original] = &duplicate
		clone.registrations = append(clone.registrations, &duplicate)
	}
	for _, original := range b.constraints {
		original.subject = copies[original.subject]
		clone.constraints = append(clone.constraints, original)
	}
	return clone
}

// Use 登记中间件到 transaction 阶段末尾（RFC-0004 规则 4）。
func (b *Builder) Use(middleware Middleware) *Builder {
	return b.UseAt(middleware, primitives.Transaction, End, AppOwner)
}

// UseAt 登记中间件到指定阶段；阶段不是十一个阶段之一时 panic KERNEL_PIPELINE_002，重复登记 panic KERNEL_PIPELINE_003
func (b *Builder) UseAt(middleware Middleware, stage primitives.PipelineStage, placement Placement, owner string) *Builder {
	return b.add(middleware, "", stage, placement, owner)
}

// UseNamedInstance 按名字登记同一类型的另一个实例（规则 7）；名字在类型内唯一，重复 panic KERNEL_PIPELINE_003
func (b *Builder) UseNamedInstance(instance string, middleware Middleware, stage primitives.PipelineStage, placement Placement, owner string) *Builder {
	return b.add(middleware, instance, stage, placement, owner)
}

// add 登记一个中间件：阶段必须合法；同一类型的默认实例只登记一次、命名实例的名字在类型内唯一（RFC-0004 规则 7），不合即 panic
func (b *Builder) add(middleware Middleware, instance string, stage primitives.PipelineStage, placement Placement, owner string) *Builder {
	if !stage.IsValid() {
		panic(primitives.NewFrameworkError(ErrorCodeUnknownStage, "Unknown pipeline stage '%d'.", int(stage)))
	}
	if owner == "" {
		owner = AppOwner
	}
	descriptor := Descriptor{Stage: stage, Middleware: middleware.Name(), Instance: instance, Owner: owner, Placement: placement}
	for _, existing := range b.registrations {
		if existing.descriptor.Middleware != descriptor.Middleware || existing.descriptor.Instance != instance {
			continue
		}
		if instance == "" {
			panic(registrationError(fmt.Sprintf("Middleware '%s' is already registered; each middleware type may appear only once in a pipeline unless its instances are named.", descriptor.Middleware)))
		}
		panic(registrationError(fmt.Sprintf("Middleware '%s' named '%s' is already registered; instance names must be unique within a type.", descriptor.Middleware, instance)))
	}
	b.registrations = append(b.registrations, &registration{middleware: middleware, descriptor: descriptor, order: len(b.registrations)})
	return b
}

// Before 约束 middleware 在 other 之前（RFC-0004 规则 7）。两端都是中间件名：命名实例的实例名，或中间件的类型名
// （该类型只登记了一个实例时）。middleware 是约束主体，声明时必须已登记，未登记或有歧义 panic KERNEL_PIPELINE_003；
// other 在排序时解析
func (b *Builder) Before(middleware, other string) *Builder {
	return b.constrain(middleware, other, true)
}

// After 约束 middleware 在 other 之后；名字的解析与报错同 Before
func (b *Builder) After(middleware, other string) *Builder {
	return b.constrain(middleware, other, false)
}

// constrain 声明一条约束：主体按名字解析成当前已登记的中间件，未登记或有歧义即 panic
func (b *Builder) constrain(middleware, other string, before bool) *Builder {
	subject, err := b.find(middleware)
	if err != nil {
		panic(err)
	}
	if subject == nil {
		panic(registrationError(fmt.Sprintf("Middleware '%s' is not registered, so no ordering constraint can be declared for it.", middleware)))
	}
	b.constraints = append(b.constraints, constraint{subject: subject, target: other, before: before})
	return b
}

// find 按名字找登记：实例名等于名字，或类型名等于名字；没有返回 nil，命中多个（同类型的多个命名实例、实例名与别的类型名相同）是登记错误
func (b *Builder) find(name string) (*registration, error) {
	var found *registration
	for _, registration := range b.registrations {
		if registration.descriptor.Instance != name && registration.descriptor.Middleware != name {
			continue
		}
		if found != nil {
			return nil, registrationError(fmt.Sprintf("The middleware name '%s' matches several registrations, so ordering constraints by name are ambiguous for it.", name))
		}
		found = registration
	}
	return found, nil
}

// Describe 返回排序后的中间件描述。
func (b *Builder) Describe() ([]Descriptor, error) {
	sorted, err := b.sorted()
	if err != nil {
		return nil, err
	}
	descriptors := make([]Descriptor, 0, len(sorted))
	for _, registration := range sorted {
		descriptors = append(descriptors, registration.descriptor)
	}
	return descriptors, nil
}

// Dump 按 xihan pipeline dump 的格式打印：一行一个中间件，阶段名左对齐 12 列。
func (b *Builder) Dump() (string, error) {
	descriptors, err := b.Describe()
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, descriptor := range descriptors {
		fmt.Fprintf(&builder, "%-12s %s\n", descriptor.Stage.ManifestName(), descriptor.Display())
	}
	return builder.String(), nil
}

// Build 构建管道：terminal 是 handle 阶段之后的终点（通常是操作分派）。
func (b *Builder) Build(terminal Step) (Step, error) {
	if terminal == nil {
		return nil, registrationError("The pipeline needs a terminal step.")
	}
	sorted, err := b.sorted()
	if err != nil {
		return nil, err
	}
	next := terminal
	for index := len(sorted) - 1; index >= 0; index-- {
		middleware := sorted[index].middleware
		downstream := next
		next = func(ctx *invocation.Context) primitives.Result[primitives.Unit] {
			return middleware.Invoke(ctx, downstream)
		}
	}
	return next, nil
}

// sorted 按规则排序（RFC-0004「排序规则」，与 .NET 的 PipelineBuilder 一致）：按阶段顺序逐阶段处理，阶段内先 Placement，再归属，
// 再登记顺序排出基准次序，然后解析这个阶段里各中间件的约束目标（按基准次序，每个中间件先 Before 后 After，各按声明顺序），
// 最后按约束做稳定拓扑排序。所以前一阶段成环先于后一阶段的约束目标未登记报出
func (b *Builder) sorted() ([]*registration, error) {
	var result []*registration
	for _, stage := range primitives.PipelineStages() {
		var members []*registration
		for _, registration := range b.registrations {
			if registration.descriptor.Stage == stage {
				members = append(members, registration)
			}
		}
		sort.SliceStable(members, func(i, j int) bool {
			left, right := members[i].descriptor, members[j].descriptor
			if left.Placement != right.Placement {
				return left.Placement < right.Placement
			}
			if left.Owner != right.Owner {
				return left.Owner < right.Owner
			}
			return members[i].order < members[j].order
		})
		edges, err := b.edges(members, stage)
		if err != nil {
			return nil, err
		}
		ordered, err := topologicalSort(members, edges, stage)
		if err != nil {
			return nil, err
		}
		result = append(result, ordered...)
	}
	return result, nil
}

// edges 解析一个阶段里的约束：目标按名字解析，未登记、有歧义或不在同一阶段都是登记错误（KERNEL_PIPELINE_003）；
// 返回的每条边 [先, 后]
func (b *Builder) edges(members []*registration, stage primitives.PipelineStage) ([][2]*registration, error) {
	var edges [][2]*registration
	for _, member := range members {
		for _, before := range []bool{true, false} {
			for _, constraint := range b.constraints {
				if constraint.subject != member || constraint.before != before {
					continue
				}
				target, err := b.find(constraint.target)
				if err != nil {
					return nil, err
				}
				subject := member.descriptor.Display()
				if target == nil {
					return nil, registrationError(fmt.Sprintf("The ordering constraint of middleware '%s' references unregistered middleware '%s'.", subject, constraint.target))
				}
				if target.descriptor.Stage != stage {
					return nil, registrationError(fmt.Sprintf("Middleware '%s' (stage '%s') and '%s' (stage '%s') are not in the same stage, so no relative order can be declared between them.", subject, stage.ManifestName(), constraint.target, target.descriptor.Stage.ManifestName()))
				}
				if before {
					edges = append(edges, [2]*registration{member, target})
				} else {
					edges = append(edges, [2]*registration{target, member})
				}
			}
		}
	}
	return edges, nil
}

// topologicalSort 在基础顺序上应用约束做稳定拓扑排序（Kahn，每次取基础顺序最靠前的无前驱节点）；成环时 KERNEL_PIPELINE_001，
// 按基础顺序列出全部仍被卡住的中间件，按名字登记的实例写成 类型名[名字]（RFC-0004 规则 8，ADR-0236）
func topologicalSort(members []*registration, edges [][2]*registration, stage primitives.PipelineStage) ([]*registration, error) {
	incoming := map[*registration]int{}
	outgoing := map[*registration][]*registration{}
	for _, edge := range edges {
		outgoing[edge[0]] = append(outgoing[edge[0]], edge[1])
		incoming[edge[1]]++
	}
	var ordered []*registration
	remaining := append([]*registration(nil), members...)
	for len(remaining) > 0 {
		picked := -1
		for position, candidate := range remaining {
			if incoming[candidate] == 0 {
				picked = position
				break
			}
		}
		if picked < 0 {
			stuck := make([]string, 0, len(remaining))
			for _, registration := range remaining {
				stuck = append(stuck, "'"+registration.descriptor.Display()+"'")
			}
			return nil, primitives.NewFrameworkError(ErrorCodeCycle, "Middleware ordering constraints in stage '%s' form a cycle: %s.", stage.ManifestName(), strings.Join(stuck, ", "))
		}
		node := remaining[picked]
		remaining = append(remaining[:picked], remaining[picked+1:]...)
		ordered = append(ordered, node)
		for _, successor := range outgoing[node] {
			incoming[successor]--
		}
	}
	return ordered, nil
}

// registrationError 创建登记错误（KERNEL_PIPELINE_003）；消息是 .NET 的英文原文，逐字相同（ADR-0236）
func registrationError(message string) error {
	return primitives.NewFrameworkError(ErrorCodeRegistration, "%s", message)
}

// ParseStage 解析清单里的阶段名；未知阶段返回 KERNEL_PIPELINE_002。
func ParseStage(name string) (primitives.PipelineStage, error) {
	stage, ok := primitives.ParsePipelineStage(name)
	if !ok {
		return 0, primitives.NewFrameworkError(ErrorCodeUnknownStage, "Unknown pipeline stage '%s'.", name)
	}
	return stage, nil
}
