#!/usr/bin/env bash
# 发版核对（RFC-0024「实现钉规范」）：打标签之前在本地跑一遍，推送标签后 release.yml 的 guard 再跑同一份。
#
# 用法：scripts/release-check.sh <标签> [<提交>]
#   提交缺省为标签指向的提交；标签还没打时为 HEAD。核对的是那个提交里的文件，不是工作区。
#
# 缺一不发：
#   ① 标签是 v<语义化版本>，带预发布后缀（v0.1.0-rc.1）的是预发布；
#   ② 提交在 origin/main 的历史里：预发布与正式版都从 main 发；
#   ③ 标签号就是提交里声明的版本：primitives/framework.go 的 SDKVersion（插件握手时上报的 SDK 版本）；
#   ④ spec 子模块正好在规范的标签上：预发布可钉规范的 vX.Y.Z-rc.N，正式版只钉规范的正式版标签；
#   ⑤ go.mod 里的一致性模块是同一提交上 conformance/vX.Y.Z 标签的版本，不是伪版本。
#
# Go 模块随标签发布，代理一旦取过就收不回，所以 ①～⑤ 要在打标签之前就核对通过。
set -euo pipefail

spec_remote="${SPEC_REMOTE:-https://github.com/XiHanFun/framework-spec.git}"
conformance_module="github.com/xihanfun/framework-spec/conformance"

fail() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::error::$1" >&2
  else
    echo "不通过：$1" >&2
  fi
  exit 1
}

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "用法：$0 <标签> [<提交>]" >&2
  exit 2
fi
tag="$1"
if [ $# -eq 2 ]; then
  commit="$(git rev-parse --verify --quiet "$2^{commit}")" || fail "找不到提交 $2。"
elif git rev-parse --verify --quiet "refs/tags/$tag" >/dev/null; then
  commit="$(git rev-parse "$tag^{commit}")"
else
  commit="$(git rev-parse HEAD)"
fi

# ① 标签的写法
if ! printf '%s' "$tag" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'; then
  fail "标签 $tag 不是 v<语义化版本>（如 v0.1.0、v0.1.0-rc.1）。"
fi
version="${tag#v}"
case "$version" in
  *-*) prerelease=true ;;
  *) prerelease=false ;;
esac
echo "通过：标签 $tag（$([ "$prerelease" = true ] && echo 预发布 || echo 正式版)），核对提交 $commit"

# ② 只从 main 发：标签本身不带分支信息，查祖先关系
git fetch --quiet --no-tags origin "+refs/heads/main:refs/remotes/origin/main" || fail "取不到 origin 的 main。"
if ! git merge-base --is-ancestor "$commit" refs/remotes/origin/main; then
  fail "提交 $commit 不在 main 的历史里。先把要发布的提交快进到 main，再在上面打标签。"
fi
echo "通过：提交在 main 的历史里"

# ③ 标签号与提交里声明的版本一致
declared="$(git show "$commit:primitives/framework.go" | sed -n 's/^[[:space:]]*SDKVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
if [ "$declared" != "$version" ]; then
  fail "标签 $tag 与 primitives/framework.go 的 SDKVersion（${declared:-没读到}）不一致。先把版本改到 $version 并快进到 main，再打标签。"
fi
echo "通过：SDKVersion 是 $version"

# ④ spec 子模块钉在规范标签上：读树里记的指针，到规范仓库的远端标签里找指向它的；
#    附注标签看剥开后的 ^{} 行，conformance/ 前缀的 Go 模块标签不算
pinned="$(git ls-tree "$commit" spec | awk '$2 == "commit" { print $3 }')"
[ -n "$pinned" ] || fail "提交里没有 spec 子模块。"
remote_tags="$(git ls-remote --tags "$spec_remote")" || fail "取不到规范仓库 $spec_remote 的标签。"
spec_tags="$(printf '%s\n' "$remote_tags" \
  | awk -v sha="$pinned" '$1 == sha { name = $2; sub(/^refs\/tags\//, "", name); sub(/\^\{\}$/, "", name); print name }' \
  | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' | sort -u || true)"
if [ "$prerelease" = false ]; then
  spec_tags="$(printf '%s\n' "$spec_tags" | grep -v -- '-' || true)"
fi
if [ -z "$spec_tags" ]; then
  fail "spec 指向的规范提交 $pinned 上没有可用的规范标签（预发布要 vX.Y.Z 或 vX.Y.Z-rc.N，正式版要 vX.Y.Z）。先给规范打标签并把 spec 指到那个标签，再发版。"
fi
spec_tag="$(printf '%s\n' "$spec_tags" | tail -n 1)"
echo "通过：spec 在规范标签 $(printf '%s\n' "$spec_tags" | tr '\n' ' ')上"

# ⑤ go.mod 的一致性模块：要是同一提交上的 conformance/ 标签，伪版本说不清实现的是哪一版规范
required="$(git show "$commit:go.mod" | awk -v module="$conformance_module" '{ for (i = 1; i < NF; i++) if ($i == module) { print $(i + 1); exit } }')"
[ -n "$required" ] || fail "go.mod 里没有 $conformance_module。"
if ! printf '%s\n' "$remote_tags" | awk -v sha="$pinned" -v ref="refs/tags/conformance/$required" '$1 == sha && ($2 == ref || $2 == ref "^{}") { found = 1 } END { exit !found }'; then
  fail "go.mod 里的 $conformance_module $required 不是 spec 所在提交 $pinned 上的 conformance/ 标签。改成 go get $conformance_module@<规范标签>，再发版。"
fi
case "$required" in
  *-*)
    if [ "$prerelease" = false ]; then
      fail "正式版的 go.mod 要钉一致性模块的正式版标签，现在是 $required。"
    fi
    ;;
esac
echo "通过：go.mod 的一致性模块是 $required"

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "version=$version"
    echo "prerelease=$prerelease"
    echo "spec_tag=$spec_tag"
  } >>"$GITHUB_OUTPUT"
fi
echo "全部通过：可以发布 $tag"
