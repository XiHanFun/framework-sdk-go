package plugin

import (
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/xihanfun/framework-spec/conformance"

	"github.com/xihanfun/framework-sdk-go/invocation"
	pluginv1 "github.com/xihanfun/framework-sdk-go/plugin/v1"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

// dataSchemas 是原样传递的 dataschema：空串是没有；带空白、不是 URI 的值 SDK 也不裁剪、不核对（invocation.md §4，ADR-0305）。
var dataSchemas = []string{"", "https://schemas.example.com/orders/v1.json", "urn:example:orders:v1", " not a uri ", "\t"}

// TestDeliveredEnvelopesCarryTheDataSchema 核对投递时宿主写来的 data_schema 原样交给处理器（invocation.md §4，ADR-0305）。
func TestDeliveredEnvelopesCarryTheDataSchema(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
		opts.Subscriptions = []SubscriptionHandler{{Subscription: Subscription{Topic: "probe.events"}, Handler: func(_ *invocation.Context, envelope Envelope, _ uint32) primitives.Result[primitives.Unit] {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, envelope.DataSchema)
			return primitives.Success()
		}}}
	})
	ctx := deadline(t)
	h.ready(ctx, nil)
	for _, schema := range dataSchemas {
		response, err := h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: &pluginv1.Envelope{Topic: "probe.events", Id: "e1", SpecVersion: "1.0", DataSchema: schema}, Mode: "competing", Attempt: 1})
		if err != nil || !response.Status.Ok {
			t.Fatalf("delivery with the data schema %q: %v %v", schema, response, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, dataSchemas) {
		t.Errorf("handlers saw the data schemas %q, want %q", seen, dataSchemas)
	}
}

// TestPublishedEnvelopesCarryTheDataSchema 核对 Host.Publish 把作者给的 DataSchema 原样写进 data_schema，没有时写空串（invocation.md §4，ADR-0305）。
func TestPublishedEnvelopesCarryTheDataSchema(t *testing.T) {
	host, address := startFakeHost(t)
	env := defaultEnv()
	env[EnvHostAddress] = address
	h, pending, results := runCallbacks(t, env, nil)
	h.ready(deadline(t), nil)
	envelopes := make([]Envelope, 0, len(dataSchemas))
	for _, schema := range dataSchemas {
		envelopes = append(envelopes, Envelope{Topic: "orders.events", ID: "m1", DataSchema: schema})
	}
	published := call(t, h, pending, results, nil, func(ctx *invocation.Context, host *Host) any {
		return host.Publish(ctx, envelopes...)
	}).([]primitives.Result[primitives.Unit])
	for index, result := range published {
		if !result.OK() {
			t.Errorf("item %d: %v", index, result.Err())
		}
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.publishes) != 1 {
		t.Fatalf("the host received %d publish requests, want 1", len(host.publishes))
	}
	var written []string
	for _, envelope := range host.publishes[0].Envelopes {
		written = append(written, envelope.DataSchema)
	}
	if !slices.Equal(written, dataSchemas) {
		t.Errorf("the host received the data schemas %q, want %q", written, dataSchemas)
	}
}

// TestEnvelopeConversionCarriesTheDataSchema 核对 ToProtoEnvelope 与 FromProtoEnvelope 往返时 DataSchema 不变。
func TestEnvelopeConversionCarriesTheDataSchema(t *testing.T) {
	for _, schema := range dataSchemas {
		encoded := ToProtoEnvelope(Envelope{Topic: "t", ID: "1", DataSchema: schema})
		if encoded.DataSchema != schema {
			t.Errorf("%q written as %q", schema, encoded.DataSchema)
		}
		if decoded, ok := FromProtoEnvelope(encoded); !ok || decoded.DataSchema != schema {
			t.Errorf("%q read back as %q (ok=%v)", schema, decoded.DataSchema, ok)
		}
	}
}

// writtenTimes 是作者给的时刻与 SDK 写出的 time：换算成 UTC、以 Z 结尾，秒的小数部分去掉末尾的 0、全为 0 时不写小数点；
// 各语言的 SDK 对同一时刻写出同一份文本（精度以内），Go 的精度是纳秒。
var writtenTimes = []struct {
	instant time.Time
	text    string
}{
	{time.Date(2026, 11, 25, 3, 25, 0, 0, time.UTC), "2026-11-25T03:25:00Z"},
	{time.Date(2026, 11, 25, 3, 25, 0, 500_000_000, time.UTC), "2026-11-25T03:25:00.5Z"},
	{time.Date(2026, 11, 25, 3, 25, 0, 120_000_000, time.UTC), "2026-11-25T03:25:00.12Z"},
	{time.Date(2026, 11, 25, 3, 25, 0, 1_000_000, time.UTC), "2026-11-25T03:25:00.001Z"},
	{time.Date(2026, 11, 25, 3, 25, 0, 123_456_000, time.UTC), "2026-11-25T03:25:00.123456Z"},
	{time.Date(2026, 11, 25, 3, 25, 0, 123_456_789, time.UTC), "2026-11-25T03:25:00.123456789Z"},
	{time.Date(2026, 11, 25, 3, 25, 0, 100, time.UTC), "2026-11-25T03:25:00.0000001Z"},
	{time.Date(2026, 11, 25, 11, 25, 0, 250_000_000, time.FixedZone("UTC+8", 8*3600)), "2026-11-25T03:25:00.25Z"},
	{time.Date(2026, 11, 24, 22, 25, 0, 0, time.FixedZone("UTC-5", -5*3600)), "2026-11-25T03:25:00Z"},
	{time.Date(2026, 11, 25, 0, 0, 0, 0, time.FixedZone("UTC+14", 14*3600)), "2026-11-24T10:00:00Z"},
	{time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), "0001-01-01T00:00:00Z"},
	{time.Date(9999, 12, 31, 23, 59, 59, 999_000_000, time.UTC), "9999-12-31T23:59:59.999Z"},
	{time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC), "9999-12-31T23:59:59.999999999Z"},
}

// TestEnvelopeTimesAreWrittenInUTC 核对 ToProtoEnvelope 写出的 time（invocation.md §4）：nil 写空串；本机时区与带单调时钟读数的时刻同样换算成 UTC。
func TestEnvelopeTimesAreWrittenInUTC(t *testing.T) {
	for _, c := range writtenTimes {
		if written := ToProtoEnvelope(Envelope{Time: &c.instant}).Time; written != c.text {
			t.Errorf("%s written as %q, want %q", c.instant, written, c.text)
		}
	}
	if written := ToProtoEnvelope(Envelope{}).Time; written != "" {
		t.Errorf("no time written as %q, want an empty string", written)
	}
	now := time.Now()
	if written, want := ToProtoEnvelope(Envelope{Time: new(now.Local())}).Time, now.UTC().Format(time.RFC3339Nano); written != want {
		t.Errorf("a local time written as %q, want %q", written, want)
	}
}

// TestEnvelopeTimesFollowTheVectors 回放 envelope-times.json（ADR-0293、ADR-0310，向量在 host 与 plugin 档）：valid 的文本经 FromProtoEnvelope 读成
// UTC 时刻（空串是 nil），invalid 读不出；written 是宿主写给插件的文本，读出的时刻等于 instant；SDK 写出的每个时刻都在 valid 的文法里、
// 读回同一时刻。Go 的精度是纳秒，九位小数不截断。
func TestEnvelopeTimesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.EnvelopeTimes()
	if err != nil {
		t.Fatal(err)
	}
	instantOf := func(text string) time.Time {
		t.Helper()
		value, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	expectRead := func(text string, want *time.Time) {
		t.Helper()
		envelope, ok := FromProtoEnvelope(&pluginv1.Envelope{Time: text})
		switch {
		case !ok:
			t.Errorf("%q must be read", text)
		case want == nil && envelope.Time != nil:
			t.Errorf("%q must be read as no time, got %s", text, envelope.Time)
		case want != nil && (envelope.Time == nil || !envelope.Time.Equal(*want) || envelope.Time.Location() != time.UTC):
			t.Errorf("%q read as %v, want %s in UTC", text, envelope.Time, want)
		}
		if envelope.Time != nil {
			written := ToProtoEnvelope(envelope).Time
			if back, ok := FromProtoEnvelope(&pluginv1.Envelope{Time: written}); !ok || back.Time == nil || !back.Time.Equal(*envelope.Time) {
				t.Errorf("%q written back as %q, which reads as %v (ok=%v)", text, written, back.Time, ok)
			}
		}
	}
	for _, c := range vectors.Valid {
		var want *time.Time
		if c.UTC != nil {
			want = new(instantOf(*c.UTC))
		}
		expectRead(c.Text, want)
	}
	for _, text := range vectors.Invalid {
		if envelope, ok := FromProtoEnvelope(&pluginv1.Envelope{Topic: "t", Time: text}); ok {
			t.Errorf("%q must not be read, got %+v", text, envelope)
		}
	}
	for _, c := range vectors.Written {
		var want *time.Time
		if c.Instant != nil {
			want = new(instantOf(*c.Instant))
		}
		expectRead(c.Text, want)
	}
	for _, c := range writtenTimes {
		expectRead(c.text, &c.instant)
	}
}

// TestSDKEnvelopeTimesFollowTheVectors 回放 sdk-envelope-times.json 的纳秒一列（invocation.md §4「SDK 读宿主写来的」「SDK 写给宿主的」，
// ADR-0306、ADR-0310）：writing 的 instant 照原文的偏移构造成 time.Time（与投递的读法读出的是同一时刻），经发布的写出路径 ToProtoEnvelope
// 写成文本、与 text 逐字比较，null 写空串；reading 的 text 经投递的读入路径 FromProtoEnvelope 读成 UTC 时刻、与 utc 相等；
// unreadable 读不出（投递以 InvalidArgument 拒绝由 TestDeliveredEnvelopesCarryTheTime 核对）。Go 的精度是纳秒，不截断。
func TestSDKEnvelopeTimesFollowTheVectors(t *testing.T) {
	vectors, err := conformance.SDKEnvelopeTimes()
	if err != nil {
		t.Fatal(err)
	}
	if vectors.Precisions.Nanoseconds != 9 {
		t.Fatalf("the nanosecond precision writes %d fractional digits, want 9", vectors.Precisions.Nanoseconds)
	}
	for _, c := range vectors.Writing {
		var at *time.Time
		if c.Instant != nil {
			// time.Parse 保留原文的偏移，写出时才换算成 UTC；先核对它与 SDK 读宿主文本的读法是同一时刻
			value, err := time.Parse(time.RFC3339Nano, *c.Instant)
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			if read, ok := FromProtoEnvelope(&pluginv1.Envelope{Time: *c.Instant}); !ok || read.Time == nil || !read.Time.Equal(value) {
				t.Errorf("%s: %q reads as %v (ok=%v), want %s", c.Name, *c.Instant, read.Time, ok, value)
			}
			at = &value
		}
		if written := ToProtoEnvelope(Envelope{Topic: "t", ID: "1", Time: at}).Time; written != c.Text.Nanoseconds {
			t.Errorf("%s: written as %q, want %q", c.Name, written, c.Text.Nanoseconds)
		}
	}
	for _, c := range vectors.Reading {
		want, err := time.Parse(time.RFC3339Nano, c.UTC.Nanoseconds)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		envelope, ok := FromProtoEnvelope(&pluginv1.Envelope{Topic: "t", Time: c.Text})
		if !ok || envelope.Time == nil || !envelope.Time.Equal(want) || envelope.Time.Location() != time.UTC {
			t.Errorf("%s: %q read as %v (ok=%v), want %s in UTC", c.Name, c.Text, envelope.Time, ok, want)
		}
	}
	for _, c := range vectors.Unreadable {
		if envelope, ok := FromProtoEnvelope(&pluginv1.Envelope{Topic: "t", Time: c.Text}); ok {
			t.Errorf("%s: %q must not be read, got %v", c.Name, c.Text, envelope.Time)
		}
	}
}

// TestDeliveredEnvelopesCarryTheTime 核对投递时 time 读成 UTC 时刻交给处理器（invocation.md §4）：空串是 nil，带偏移的换算成 UTC；
// 读不出的投递以 gRPC InvalidArgument 拒绝，不找订阅、不交给处理器。
func TestDeliveredEnvelopesCarryTheTime(t *testing.T) {
	var mu sync.Mutex
	var seen []*time.Time
	h := launch(t, defaultEnv(), func(opts *Options, _ *recorder) {
		opts.Subscriptions = []SubscriptionHandler{{Subscription: Subscription{Topic: "probe.events"}, Handler: func(_ *invocation.Context, envelope Envelope, _ uint32) primitives.Result[primitives.Unit] {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, envelope.Time)
			return primitives.Success()
		}}}
	})
	ctx := deadline(t)
	h.ready(ctx, nil)
	deliver := func(topic, text string) (*pluginv1.HandleEventResponse, error) {
		return h.client.HandleEvent(ctx, &pluginv1.HandleEventRequest{Envelope: &pluginv1.Envelope{Topic: topic, Id: "e1", SpecVersion: "1.0", Time: text}, Mode: "competing", Attempt: 1})
	}
	for _, text := range []string{"2026-11-25T11:25:00.5000000+08:00", "", "2026-11-25T03:25:00.123456789Z"} {
		if response, err := deliver("probe.events", text); err != nil || !response.Status.Ok {
			t.Fatalf("delivery with the time %q: %v %v", text, response, err)
		}
	}
	// 主题没有订阅也是 InvalidArgument：time 在找订阅之前核对；sdk-envelope-times.json 的 unreadable 每条都经投递核对（ADR-0310）
	unreadable := []string{"2026-12-31T23:59:60Z", "2026-11-25T03:25:00", "2026-11-25T03:25:00.1234567890Z", "0001-01-01T00:00:00+00:01"}
	vectors, err := conformance.SDKEnvelopeTimes()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Unreadable {
		unreadable = append(unreadable, c.Text)
	}
	for _, topic := range []string{"probe.events", "other.events"} {
		for _, text := range unreadable {
			if response, err := deliver(topic, text); status.Code(err) != codes.InvalidArgument {
				t.Errorf("delivery on %s with the time %q: %v %v, want InvalidArgument", topic, text, response, err)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []*time.Time{new(time.Date(2026, 11, 25, 3, 25, 0, 500_000_000, time.UTC)), nil, new(time.Date(2026, 11, 25, 3, 25, 0, 123_456_789, time.UTC))}
	if len(seen) != len(want) {
		t.Fatalf("handlers saw %d deliveries, want %d", len(seen), len(want))
	}
	for index := range want {
		got := seen[index]
		if (got == nil) != (want[index] == nil) || got != nil && (!got.Equal(*want[index]) || got.Location() != time.UTC) {
			t.Errorf("delivery %d carried the time %v, want %v", index, got, want[index])
		}
	}
}

// TestPublishedEnvelopesCarryTheTime 核对 Host.Publish 把作者给的时刻换算成 UTC 写出、没有时写空串（invocation.md §4）；
// 越界的时刻 SDK 不核对，照写给宿主（宿主拒绝那一项）。
func TestPublishedEnvelopesCarryTheTime(t *testing.T) {
	host, address := startFakeHost(t)
	env := defaultEnv()
	env[EnvHostAddress] = address
	h, pending, results := runCallbacks(t, env, nil)
	h.ready(deadline(t), nil)
	envelopes := []Envelope{
		{Topic: "orders.events", ID: "m1", Time: new(time.Date(2026, 11, 25, 11, 25, 0, 250_000_000, time.FixedZone("UTC+8", 8*3600)))},
		{Topic: "orders.events", ID: "m2"},
		{Topic: "orders.events", ID: "m3", Time: new(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))},
	}
	published := call(t, h, pending, results, nil, func(ctx *invocation.Context, host *Host) any {
		return host.Publish(ctx, envelopes...)
	}).([]primitives.Result[primitives.Unit])
	if len(published) != len(envelopes) {
		t.Fatalf("%d results for %d envelopes", len(published), len(envelopes))
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.publishes) != 1 {
		t.Fatalf("the host received %d publish requests, want 1", len(host.publishes))
	}
	var written []string
	for _, envelope := range host.publishes[0].Envelopes {
		written = append(written, envelope.Time)
	}
	if want := []string{"2026-11-25T03:25:00.25Z", "", "10000-01-01T00:00:00Z"}; !slices.Equal(written, want) {
		t.Errorf("the host received the times %q, want %q", written, want)
	}
}
