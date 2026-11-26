---
name: xihan-sdk-go-remote-provider
description: 设计、实现、审查或诊断 framework-sdk-go 的 caching 包（caching.distributed@1 远程 Provider 的插件侧）与 examples/cache 时使用，适用于存储契约、操作集、有界内存实现、批量语义与新后端；插件运行时本身用 plugin-runtime 技能，宿主一侧的适配器在 XiHan.Framework 里改。
---

# framework-sdk-go 远程 Provider

## 规范与参照

- RFC-0019《远程 Provider》与 `spec/provider/README.md`（通用规则：插件清单不写 `provides`、操作 ID 固定、握手声明 `stability: internal` 且 `expose` 为空、两侧都校验输入、原子性由插件保证）。
- `spec/provider/caching-distributed.md`（操作、字段、语义、错误、清单与握手）、RFC-0006 的存储契约、`spec/conventions/batch-first.md`。
- 向量 `spec/conformance/vectors/provider/caching-distributed.json` 与 `spec/conformance/vectors/caching/cache-keys.json`（键的文法：长度按 UTF-8 字节计、空白与控制字符），档 `provider:caching.distributed@1`。
- 宿主一侧：XiHan.Framework 的 `XiHan.Framework.Caching.Remote` 适配器与 `XiHan.Framework.Caching.Conformance` 套件。

## 本 SDK 的位置

| 文件 | 内容 |
| --- | --- |
| `caching/store.go` | `Store` 接口（RFC-0006 的六个存储操作加三个批量操作）、操作 ID、错误码 `CACHING_*`、消息键 `caching.remote.*`、键与选项的校验（键的规则是一致性模块的 `rules.IsCacheKey`：非空、不含空白、控制字符与孤立的代理项（Go 里即必须是合法的 UTF-8）、按 UTF-8 编码后不超过 512 字节；`examples/cache/Localization` 的模板逐字照 .NET `Caching.Remote` 的目录，不一致时宿主报 `LOCALIZATION_CONFLICT_001`） |
| `caching/memory.go` | `MemoryStore`：互斥锁串行化，读取时判定过期与延长滑动过期，`DefaultCapacity` / `WithCapacity` 限制条目数，满了先清过期条目、仍满则 `CACHING_UNAVAILABLE_001` |
| `caching/operations.go` | `RegisterOperations` 把任何 `Store` 登记成操作集；请求与响应结构 |
| `examples/cache` | 插件示例：清单 `xihan.manifest.json`、配置节 `XiHan:Cache`（`defaultTimeToLiveSeconds`、`maxEntries`）、目录 `Localization/`（`en-US`、`zh-CN`）、入口 `cmd/cache-go` |

## 约定

- 换后端只需实现一个 `caching.Store`，操作集与插件运行时不变；`Store` 返回的错误应是 `*primitives.Error`，其他错误按后端不可用处理。
- 批量方法一次完成，结果与入参等长、顺序一致，空集合不触后端，基础设施失败整批报错；`set-many` 任一项不合法时整批不写入并带上 `index`。
- 存储必须有界，容量、过期与清理语义要有测试；不写无界缓存。
- 原子操作（`try-add`、`try-remove`、`try-refresh`）的比较与写入之间不得插入其他写入。
- `examples/cache` 的目录名 `caching.remote` 与宿主一侧 `XiHan.Framework.Caching.Remote` 的目录同名、键也相同：宿主按 RFC-0010「归属目录」核对同名目录，同一键同一区域的模板不同即 `LOCALIZATION_CONFLICT_001`、插件整组登记不进去（`plugin/handshake.md` §3 第 6 条，ADR-0252）。改 `examples/cache/Localization/` 里的模板要与 .NET 的 `Caching.Remote/Localization/` 逐字相同，并与 `framework/samples/CacheGoPlugin` 升级本 SDK 放在同一次改动里。

## 验证

1. `go test ./caching/...`：`TestOperationsReplayTheVectors` 回放操作集的向量，`TestKeysFollowTheVectors` 回放键的向量，其余用例覆盖过期、滑动、原子操作、批量与容量。
2. `go test ./examples/cache/...`：用 `go.mod` 版本的黑盒运行器跑协议层全部用例，只有 `event`（没有订阅）是名单内的跳过。
3. 操作集的行为变化还要在 XiHan.Framework 里验证：`framework/samples/CacheGoPlugin` 的 `go.mod` 锁定本 SDK 的版本，用 `go get github.com/xihanfun/framework-sdk-go@<提交或标签>` 升级后跑 `XiHan.Framework.Caching.Remote.Tests`（`RemoteCacheStoreConformanceTests` 让适配器加插件跑完整套 `Caching.Conformance`）。那是另一个仓库，本 SDK 的提交推送之后才能升级，单独提交。
