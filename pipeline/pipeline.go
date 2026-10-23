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

// constraint 是一条顺序约束：first 在 second 之前。
type constraint struct {
	first, second string
}

// Builder 收集中间件与顺序约束，按规范排序后构建管道。
type Builder struct {
	registrations []*registration
	constraints   []constraint
}

// NewBuilder 创建空的构建器。
func NewBuilder() *Builder { return &Builder{} }

// Use 登记中间件到 transaction 阶段末尾（RFC-0004 规则 4）。
func (b *Builder) Use(middleware Middleware) *Builder {
	return b.UseAt(middleware, primitives.Transaction, End, AppOwner)
}

// UseAt 登记中间件到指定阶段。
func (b *Builder) UseAt(middleware Middleware, stage primitives.PipelineStage, placement Placement, owner string) *Builder {
	return b.add(middleware, "", stage, placement, owner)
}

// UseNamedInstance 按名字登记同一类型的另一个实例（规则 7）；名字在类型内唯一。
func (b *Builder) UseNamedInstance(instance string, middleware Middleware, stage primitives.PipelineStage, placement Placement, owner string) *Builder {
	return b.add(middleware, instance, stage, placement, owner)
}

// add 登记一个中间件。
func (b *Builder) add(middleware Middleware, instance string, stage primitives.PipelineStage, placement Placement, owner string) *Builder {
	if owner == "" {
		owner = AppOwner
	}
	b.registrations = append(b.registrations, &registration{
		middleware: middleware,
		descriptor: Descriptor{Stage: stage, Middleware: middleware.Name(), Instance: instance, Owner: owner, Placement: placement},
		order:      len(b.registrations),
	})
	return b
}

// Before 约束 middleware 在 other 之前；都按类型名。
func (b *Builder) Before(middleware, other string) *Builder {
	b.constraints = append(b.constraints, constraint{first: middleware, second: other})
	return b
}

// After 约束 middleware 在 other 之后；都按类型名。
func (b *Builder) After(middleware, other string) *Builder {
	b.constraints = append(b.constraints, constraint{first: other, second: middleware})
	return b
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
		return nil, registrationError("the pipeline needs a terminal step")
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

// sorted 按规则排序：先阶段；阶段内先 Placement，再归属，再登记顺序，最后按约束做稳定拓扑排序。
func (b *Builder) sorted() ([]*registration, error) {
	if err := b.checkDuplicates(); err != nil {
		return nil, err
	}
	byType := map[string][]*registration{}
	for _, registration := range b.registrations {
		byType[registration.descriptor.Middleware] = append(byType[registration.descriptor.Middleware], registration)
	}
	// 约束按类型名引用：未注册、有多个实例、跨阶段都是登记错误
	resolved := make([][2]*registration, 0, len(b.constraints))
	for _, constraint := range b.constraints {
		first, err := b.single(byType, constraint.first)
		if err != nil {
			return nil, err
		}
		second, err := b.single(byType, constraint.second)
		if err != nil {
			return nil, err
		}
		if first.descriptor.Stage != second.descriptor.Stage {
			return nil, registrationError(fmt.Sprintf("the ordering constraint between '%s' and '%s' crosses stages", constraint.first, constraint.second))
		}
		resolved = append(resolved, [2]*registration{first, second})
	}

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
		ordered, err := topologicalSort(members, resolved)
		if err != nil {
			return nil, err
		}
		result = append(result, ordered...)
	}
	return result, nil
}

// checkDuplicates 核对同一类型的默认实例只登记一次、命名实例的名字在类型内唯一。
func (b *Builder) checkDuplicates() error {
	seen := map[string]bool{}
	for _, registration := range b.registrations {
		key := registration.descriptor.Middleware + "\x00" + registration.descriptor.Instance
		if seen[key] {
			return registrationError(fmt.Sprintf("the middleware '%s' is registered more than once", registration.descriptor.Display()))
		}
		seen[key] = true
	}
	return nil
}

// single 按类型名取唯一的登记；没有或有多个实例都是登记错误。
func (b *Builder) single(byType map[string][]*registration, name string) (*registration, error) {
	registrations := byType[name]
	switch len(registrations) {
	case 0:
		return nil, registrationError(fmt.Sprintf("the ordering constraint references the unregistered middleware '%s'", name))
	case 1:
		return registrations[0], nil
	default:
		return nil, registrationError(fmt.Sprintf("the middleware '%s' has several named instances; ordering constraints by type are ambiguous", name))
	}
}

// topologicalSort 在基础顺序上应用约束做稳定拓扑排序（Kahn，每次取基础顺序最靠前的无前驱节点）。
func topologicalSort(members []*registration, constraints [][2]*registration) ([]*registration, error) {
	index := map[*registration]int{}
	for position, member := range members {
		index[member] = position
	}
	incoming := map[*registration]int{}
	outgoing := map[*registration][]*registration{}
	for _, constraint := range constraints {
		first, second := constraint[0], constraint[1]
		if _, ok := index[first]; !ok {
			continue
		}
		outgoing[first] = append(outgoing[first], second)
		incoming[second]++
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
			return nil, &primitives.FrameworkError{Code: ErrorCodeCycle, Message: fmt.Sprintf("the ordering constraints in stage '%s' form a cycle", remaining[0].descriptor.Stage)}
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

// registrationError 创建登记错误。
func registrationError(message string) error {
	return &primitives.FrameworkError{Code: ErrorCodeRegistration, Message: message + "."}
}

// ParseStage 解析清单里的阶段名；未知阶段返回 KERNEL_PIPELINE_002。
func ParseStage(name string) (primitives.PipelineStage, error) {
	stage, ok := primitives.ParsePipelineStage(name)
	if !ok {
		return 0, &primitives.FrameworkError{Code: ErrorCodeUnknownStage, Message: fmt.Sprintf("'%s' is not a pipeline stage.", name)}
	}
	return stage, nil
}
