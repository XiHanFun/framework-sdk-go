package plugin

import (
	"slices"
	"sync"
	"testing"

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
		if decoded := FromProtoEnvelope(encoded); decoded.DataSchema != schema {
			t.Errorf("%q read back as %q", schema, decoded.DataSchema)
		}
	}
}
