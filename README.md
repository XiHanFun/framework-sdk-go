<div align="center">

<h1>XiHan Framework SDK for Go</h1>

<h3>Out-of-process plugins and remote providers in Go, verified by the same conformance vectors as the .NET host</h3>

<p>
  <a href="https://github.com/XiHanFun/framework-sdk-go/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/XiHanFun/framework-sdk-go/ci.yml?branch=main&style=flat-square&label=CI" /></a>
  <a href="https://pkg.go.dev/github.com/xihanfun/framework-sdk-go"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/xihanfun/framework-sdk-go.svg" /></a>
  <img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/XiHanFun/framework-sdk-go?style=flat-square&logo=go&logoColor=white&color=00ADD8" />
  <a href="./LICENSE"><img alt="MIT License" src="https://img.shields.io/github/license/XiHanFun/framework-sdk-go?style=flat-square&color=green" /></a>
  <a href="https://github.com/XiHanFun/framework-sdk-go/stargazers"><img alt="GitHub Stars" src="https://img.shields.io/github/stars/XiHanFun/framework-sdk-go?style=flat-square&logo=github&label=Stars&color=1f6feb" /></a>
</p>

<p>
  <a href="https://framework.docs.xihanfun.com/sdks/go"><b>Docs</b></a> ·
  <a href="#getting-started"><b>Getting Started</b></a> ·
  <a href="https://github.com/XiHanFun/framework-spec"><b>Specification</b></a> ·
  <a href="https://github.com/XiHanFun/XiHan.Framework"><b>Reference Implementation</b></a>
</p>

<p><b>English</b> | <a href="./README_cn.md">简体中文</a></p>

</div>

The Go implementation of [XiHan Spec](https://github.com/XiHanFun/framework-spec): the primitives layer, manifests, the invocation context, the pipeline, operation contracts and the out-of-process plugin runtime, plus the plugin side of the `caching.distributed@1` remote provider. Use it to write Go plugins that run in the .NET host ([XiHan.Framework](https://github.com/XiHanFun/XiHan.Framework)), or to use the same primitives in Go applications; the spec lives in `spec/` as a submodule, and conformance is verified by the vectors and black-box runner of the spec repository.

## Modules

| Package | Contents | Spec |
| --- | --- | --- |
| `primitives` | `Error` and error codes, `Result[T]`, `Maybe[T]`, `ID[Tag, V]`, lifecycle, pipeline stages, Baggage keys, message keys and templates | RFC-0001, RFC-0010 |
| `manifest` | The `xihan.manifest.json` model, reading, canonical output and validation | RFC-0002 |
| `invocation` | The invocation context: cancellation and deadlines, W3C tracing, registered Baggage | RFC-0003 |
| `pipeline` | The eleven-stage pipeline and its ordering rules | RFC-0004 |
| `operation` | Operation contracts, the registry, and an invoker that runs through the full pipeline | RFC-0011 |
| `plugin` | The out-of-process plugin runtime `Serve` and the host callback client `Host` | RFC-0017 |
| `plugin/v1` | Protocol code generated from `spec/plugin/plugin.proto` | `plugin/plugin.proto` |
| `caching` | The plugin side of the `caching.distributed@1` remote provider: the store contract, a bounded in-memory store and the operation set | RFC-0019 |
| `examples/echo` | An echo plugin; `cmd/echo-go` is the executable entry point | — |
| `examples/cache` | A remote cache provider plugin; `cmd/cache-go` is the executable entry point | — |

## Installation

```bash
go get github.com/xihanfun/framework-sdk-go
```

## Getting Started

```go
package main

import (
	"context"
	_ "embed"
	"os"

	"github.com/xihanfun/framework-sdk-go/invocation"
	"github.com/xihanfun/framework-sdk-go/operation"
	"github.com/xihanfun/framework-sdk-go/plugin"
	"github.com/xihanfun/framework-sdk-go/primitives"
)

//go:embed xihan.manifest.json
var manifest []byte

type CreateRequest struct{ Text string `json:"text"` }
type CreateResponse struct{ Text, Tenant string }

func main() {
	registry := operation.NewRegistry()
	_ = operation.Add(registry, operation.Contract{ID: "echoes.create", Stability: primitives.Experimental, Expose: []string{operation.ExposeHTTP}},
		operation.HandlerFunc[CreateRequest, CreateResponse](func(ctx *invocation.Context, request CreateRequest) primitives.Result[CreateResponse] {
			return primitives.Ok(CreateResponse{Text: request.Text, Tenant: ctx.Tenant()})
		}), nil, "")

	if err := plugin.Serve(context.Background(), plugin.Options{Manifest: manifest, Operations: registry}); err != nil {
		os.Exit(1)
	}
}
```

Put the manifest `xihan.manifest.json` in the same directory as the built executable and the host can load it; verify it with the black-box runner of the spec repository, `xihan-conformance plugin --dir <output-dir>`. How to write the manifest, message catalogs, subscriptions, callbacks and logs is covered in [Write a plugin](https://framework.docs.xihanfun.com/sdks/go#写一个插件) in the docs.

## Documentation

The documentation site is in Chinese.

| Where | Contents |
| --- | --- |
| [Go SDK](https://framework.docs.xihanfun.com/sdks/go) | Modules, writing a plugin, behavior shared with the host, language mapping, conformance, breaking changes and migration |
| [Out-of-process plugins](https://framework.docs.xihanfun.com/plugins/out-of-process) | The protocol, handshake and lifecycle, loading and callbacks on the host side, remote providers |
| [Spec and languages](https://framework.docs.xihanfun.com/appendix/spec) | How the spec is organized, conformance profiles, the state of each language SDK |
| [XiHan Spec](https://github.com/XiHanFun/framework-spec) | RFCs, vocabulary, protocol mappings, conformance vectors and the black-box runner |

## Status

Claimed conformance profiles (RFC-0024): `core`, `plugin` and `provider:caching.distributed@1`, with no gaps; the pinned spec version is the one in the `spec/` submodule and the conformance module in `go.mod`. The maturity is Experimental: there is a prerelease pinned to a spec rc tag, and Stable also needs a release pinned to a formal spec tag; streaming operations and the Wasm carrier are out of scope. Details are in [Conformance](https://framework.docs.xihanfun.com/sdks/go#一致性) in the docs.

## Ecosystem

| Project | Description |
| --- | --- |
| [framework-spec](https://github.com/XiHanFun/framework-spec) | XiHan Spec: RFCs, vocabulary, protocol mappings and conformance vectors |
| [XiHan.Framework](https://github.com/XiHanFun/XiHan.Framework) | The .NET reference implementation |
| [framework-sdk-typescript](https://github.com/XiHanFun/framework-sdk-typescript) | TypeScript SDK |
| [framework-sdk-rust](https://github.com/XiHanFun/framework-sdk-rust) | Rust SDK |
| [framework-sdk-java](https://github.com/XiHanFun/framework-sdk-java) | Java SDK |
| [framework-sdk-python](https://github.com/XiHanFun/framework-sdk-python) | Python SDK |
| [framework-sdk-php](https://github.com/XiHanFun/framework-sdk-php) | PHP SDK |

## Contributing

[Issues](https://github.com/XiHanFun/framework-sdk-go/issues) and pull requests are welcome. Changes to public contracts are not made in the SDK repositories: they go through the RFC process of framework-spec first and are then synced to each SDK. Development conventions, gates and the release process are in [AGENTS.md](./AGENTS.md).

## License

[MIT](./LICENSE) © 2021-Present XiHanFun and contributors.
