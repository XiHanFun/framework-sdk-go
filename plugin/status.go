package plugin

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xihanfun/framework-spec/conformance/rules"

	"github.com/xihanfun/framework-sdk-go/internal/jsoncodec"
	"github.com/xihanfun/framework-sdk-go/invocation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// ToProtoError 把原语层错误编码为协议的 Error（invocation.md §3「写出」）：code 与 message_key 原样；带消息键的错误 text 留空，
// 由宿主按目录翻译成调用方的语言（RFC-0010），只有自由文本错误（Literal）写 text；details 是按值模型写出的 JSON 对象文本
// （整数值的浮点带小数点，非有限值写成字符串，ADR-0236），没有时为空；inner 递归写出。
//
// 细节写不出时这个错误不带细节、details 为空，其余字段照写（RFC-0001「写出与转交」，ADR-0320）：细节里的字符串（成员名与值）不是
// 合法的 UTF-8（含编成 UTF-8 的孤立代理项），写出的文本没过共同检查（嵌套超过 64 层、重复的成员名、原样嵌进去的孤立代理项转义），
// 或值写不出（json.Number 不是数字等）。不写成 U+FFFD 或 \ud800 这样的转义：宿主读到这样的细节也当作没有，两边看到的相同；
// 内部错误的细节各自核对。
//
// text 是自由文本，编不成合法 UTF-8 的部分按最大子部分换成 U+FFFD 写出（invocation.md §3「写出 Error 的文案」，ADR-0339，见 scrubText），
// code、message_key、details 与 inner 照上面写：protobuf-go 拒绝编码 string 字段里非法的 UTF-8，以前整个回调、发布与应答都写不出
func ToProtoError(err *primitives.Error) *pluginv1.Error {
	if err == nil {
		return nil
	}
	encoded := &pluginv1.Error{Code: err.Code, MessageKey: err.MessageKey}
	if err.MessageKey == "" {
		encoded.Text = scrubText(err.Message())
	}
	if len(err.Details) > 0 {
		if details, marshalErr := jsoncodec.MarshalChecked(err.Details); marshalErr == nil {
			encoded.Details = string(details)
		}
	}
	if err.Inner != nil {
		encoded.Inner = ToProtoError(err.Inner)
	}
	return encoded
}

// scrubText 把文案里编不成合法 UTF-8 的部分换成 U+FFFD（invocation.md §3「写出 Error 的文案」，ADR-0339）：按 Unicode §3.9 的最大子部分，
// 从不能解码的位置起，能成为某个合法序列开头的最长前缀是一段，不能的一个字节是一段，每一段换一个 U+FFFD；合法的字符（含本来就是
// U+FFFD 的）照抄。结果与 .NET 的 Encoding.UTF8.GetString、Python 的 bytes.decode('utf-8', 'replace') 相同。
func scrubText(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		// 解码出 RuneError 且只占一个字节才是不能解码；占三个字节的是本来就写着的 U+FFFD
		if r != utf8.RuneError || size > 1 {
			b.WriteString(text[i : i+size])
			i += size
			continue
		}
		// DecodeRuneInString 遇到不能解码的只吃一个字节，strings.ToValidUTF8 把连着的一串换成一个，都不是最大子部分：往后加字节，
		// 直到 FullRuneInString 认为前缀已完整（首字节不能开头、某个后续字节越界）或到了最长的四个字节。因最后加进来的字节越界才完整的，
		// 那个字节不属于这一段，退回去留给下一轮；到了文本末尾仍不完整的前缀整段算一个
		n := 1
		for n < len(text)-i && n < utf8.UTFMax && !utf8.FullRuneInString(text[i:i+n]) {
			n++
		}
		if n > 1 && utf8.FullRuneInString(text[i:i+n]) {
			n--
		}
		b.WriteRune(utf8.RuneError)
		i += n
	}
	return b.String()
}

// FromProtoError 按 invocation.md §3「读入」还原宿主写来的 Error（规则就是一致性模块的 rules.ReadProtoError，向量 error-reading.json）；
// pluginID 是插件的清单 ID：
//   - code 不合错误码文法时整个错误换成 PLUGINS_UNEXPECTED_001 + plugins.process.failed（phase 一律为 invoke，reason 为
//     invalid error code '<code>'），原来的 inner 丢掉；
//   - message_key 合消息键文法的按键还原、不取 text，否则是自由文本错误，文案取 text，text 为空或只有空白（RFC-0001「空白字符」）时取 code；
//   - details 为空、只有空白、是空对象、通不过共同的 JSON 读取检查（RFC-0001）、不是对象，或任何一层有读成双精度会溢出的数时当作没有细节，
//     否则按值模型读（64 位范围内整数写法的数字是 int64，其余是 float64，下溢读成 0，数组与对象是 []any 与 map[string]any）；
//     inner 按同样的规则递归。
func FromProtoError(err *pluginv1.Error, pluginID string) *primitives.Error {
	if err == nil {
		return nil
	}
	return fromDecoded(rules.ReadProtoError(toRulesError(err), pluginID))
}

// toRulesError 把协议的 Error 换成规则函数的输入。
func toRulesError(err *pluginv1.Error) rules.ProtoError {
	converted := rules.ProtoError{Code: err.Code, MessageKey: err.MessageKey, Text: err.Text, Details: err.Details}
	if err.Inner != nil {
		inner := toRulesError(err.Inner)
		converted.Inner = &inner
	}
	return converted
}

// fromDecoded 把规则函数读出的错误换成原语层错误：带键的按键，否则是 Literal（ReadProtoError 保证自由文本错误的文案不为空、也不只有空白）。
func fromDecoded(decoded rules.DecodedError) *primitives.Error {
	var inner *primitives.Error
	if decoded.Inner != nil {
		inner = fromDecoded(*decoded.Inner)
	}
	if decoded.Key != "" {
		return &primitives.Error{Code: decoded.Code, MessageKey: decoded.Key, Details: decoded.Details, Inner: inner}
	}
	return primitives.Literal(decoded.Code, decoded.Text, decoded.Details, inner)
}

// ToStatus 把结果编码为协议的 Status。
func ToStatus(result primitives.Result[primitives.Unit]) *pluginv1.Status {
	if result.OK() {
		return &pluginv1.Status{Ok: true}
	}
	return &pluginv1.Status{Ok: false, Error: ToProtoError(result.Err())}
}

// FromStatus 还原宿主写来的 Status（invocation.md §3，规则就是 rules.ReadProtoStatus，插件读宿主）：错误按 FromProtoError 读；
// ok 为假却没有 error、或整个 status 缺失时为 PLUGINS_UNEXPECTED_001 + plugins.process.failed，phase 是 phase（回调的操作 ID 或 publish，
// 见 rules.CallbackPhase），reason 为 the host reported a failure without an error。
func FromStatus(status *pluginv1.Status, pluginID, phase string) primitives.Result[primitives.Unit] {
	var failure *rules.ProtoError
	if status.GetError() != nil {
		converted := toRulesError(status.GetError())
		failure = &converted
	}
	decoded := rules.ReadProtoStatus(status.GetOk(), failure, rules.ReaderPlugin, phase, pluginID)
	if decoded == nil {
		return primitives.Success()
	}
	return primitives.Fail[primitives.Unit](fromDecoded(*decoded))
}

// FromMetadata 从协议的上下文元数据还原调用上下文（invocation.md §2「接收方还原上下文」）；parent 承载 gRPC 的取消与截止时间：
//   - trace_parent 按 RFC-0003 的文法取，不合文法即没有父 Span；trace_state 不进上下文；
//   - baggage 只保留已登记的键（conventions/baggage-keys.md），值不能进入上下文的丢掉（invocation.WithBaggage）；
//   - deadline 按严格的文法读（rules.ParseDeadline），读不出当作没有；生效的截止时间取它与 parent 的截止时间中较早的一个。
//
// 同 invocation.New，带截止时间的上下文到截止时间或 parent 取消才归还定时器；运行时自己的调用用 invocation.NewWithCancel，结束时释放。
func FromMetadata(parent context.Context, metadata *pluginv1.InvocationMetadata) *invocation.Context {
	return invocation.New(parent, metadataOptions(parent, metadata)...)
}

// metadataOptions 把协议的上下文元数据换成调用上下文的选项，规则见 FromMetadata。
func metadataOptions(parent context.Context, metadata *pluginv1.InvocationMetadata) []invocation.Option {
	options := []invocation.Option{}
	if traceParent := metadata.GetTraceParent(); traceParent != "" {
		options = append(options, invocation.WithTraceParent(traceParent))
	}
	baggage := map[string]string{}
	for key, value := range metadata.GetBaggage() {
		if primitives.IsBaggageKeyRegistered(key) {
			baggage[key] = value
		}
	}
	if len(baggage) > 0 {
		options = append(options, invocation.WithBaggage(baggage))
	}
	// 元数据的写法不合法时不能连 gRPC 自身的截止时间一起丢掉：两者各自读，取较早的
	grpcDeadline, hasGRPC := parent.Deadline()
	if deadline, ok := rules.EffectiveDeadline(metadata.GetDeadline(), grpcDeadline, hasGRPC); ok {
		// invocation.Context 以零值的 time.Time 表示没有截止时间：恰好是 0001-01-01T00:00:00Z（文法允许的最早时刻，早已过期）的截止时间
		// 记成晚 1 纳秒，仍是过期，不变成没有截止时间；写出时截到 100 纳秒，与读到的文本相同
		if deadline.IsZero() {
			deadline = deadline.Add(time.Nanosecond)
		}
		options = append(options, invocation.WithDeadline(deadline))
	}
	return options
}

// ToMetadata 把调用上下文编码为协议的上下文元数据（invocation.md §2、§5）：trace_parent 是当前 Span 的 traceparent；
// deadline 按 .NET 的 O 格式写（UTC、七位小数、+00:00，rules.FormatDeadline）；baggage 只有已登记的键（RFC-0003 规则 3）；trace_state 为空。
func ToMetadata(ctx *invocation.Context) *pluginv1.InvocationMetadata {
	metadata := &pluginv1.InvocationMetadata{TraceParent: ctx.TraceParent(), Baggage: map[string]string{}}
	if deadline, ok := ctx.Deadline(); ok {
		metadata.Deadline = rules.FormatDeadline(deadline)
	}
	for key, value := range ctx.WithoutUnregisteredBaggage().Baggage() {
		metadata.Baggage[key] = value
	}
	return metadata
}

// ToProtoEnvelope 把信封编码为协议消息：Time 按 formatEnvelopeTime 写出，DataSchema 原样写进 data_schema（没有即空串，invocation.md §4）。
func ToProtoEnvelope(envelope Envelope) *pluginv1.Envelope {
	specVersion := envelope.SpecVersion
	if specVersion == "" {
		specVersion = "1.0"
	}
	contentType := envelope.DataContentType
	if contentType == "" {
		contentType = "application/json"
	}
	return &pluginv1.Envelope{
		Topic:           envelope.Topic,
		Id:              envelope.ID,
		Source:          envelope.Source,
		Type:            envelope.Type,
		SpecVersion:     specVersion,
		Subject:         envelope.Subject,
		Time:            formatEnvelopeTime(envelope.Time),
		DataContentType: contentType,
		Data:            envelope.Data,
		Extensions:      envelope.Extensions,
		DataSchema:      envelope.DataSchema,
	}
}

// FromProtoEnvelope 把宿主写来的协议消息还原为信封：time 按规范的文法读成 UTC 时刻（rules.ReadEnvelopeTime，空串即没有），
// data_schema 原样交出（空串即没有，invocation.md §4）。time 不合文法或越界时 ok 为假、信封为零值；nil 是没有字段的信封。
func FromProtoEnvelope(envelope *pluginv1.Envelope) (Envelope, bool) {
	if envelope == nil {
		return Envelope{}, true
	}
	instant, present, ok := rules.ReadEnvelopeTime(envelope.Time)
	if !ok {
		return Envelope{}, false
	}
	// 没有时间是 nil，不是零值：0001-01-01T00:00:00Z 正是零值的 time.Time，也是文法允许的最早时刻
	var at *time.Time
	if present {
		at = &instant
	}
	return Envelope{
		Topic:           envelope.Topic,
		ID:              envelope.Id,
		Source:          envelope.Source,
		Type:            envelope.Type,
		SpecVersion:     envelope.SpecVersion,
		Subject:         envelope.Subject,
		Time:            at,
		DataContentType: envelope.DataContentType,
		DataSchema:      envelope.DataSchema,
		Data:            envelope.Data,
		Extensions:      envelope.Extensions,
	}, true
}

// formatEnvelopeTime 写出信封的时刻（invocation.md §4）：nil 写空串；否则换算成 UTC、按 time.RFC3339Nano 写，
// 以 Z 结尾，秒的小数部分去掉末尾的 0、全为 0 时不写小数点，各语言的 SDK 对同一时刻写出同一份文本（精度以内）。
// 不核对范围：越界的时刻照写，由宿主逐项核对时拒绝，不改变它与其余核对的先后（ADR-0293）。
func formatEnvelopeTime(instant *time.Time) string {
	if instant == nil {
		return ""
	}
	return instant.UTC().Format(time.RFC3339Nano)
}

// toProtoHealth 把健康编码为协议消息。
func toProtoHealth(health Health) *pluginv1.HealthResponse {
	response := &pluginv1.HealthResponse{State: pluginv1.LifecycleState(health.State), Error: ToProtoError(health.Error)}
	for _, item := range health.Items {
		response.Items = append(response.Items, &pluginv1.HealthItem{Name: item.Name, Status: pluginv1.HealthStatus(item.Status), Description: item.Description})
	}
	return response
}
