package cache_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/examples/cache"
)

// conformanceModule 是规范仓库的一致性模块；黑盒运行器从它安装。
const conformanceModule = "github.com/xihanfun/framework-spec/conformance"

// result 是运行器 --json 输出的一项。
type result struct {
	Case       string `json:"case"`
	Passed     bool   `json:"passed"`
	Skipped    bool   `json:"skipped"`
	Message    string `json:"message"`
	DurationMs int64  `json:"durationMs"`
}

// TestCachePassesTheConformanceSuite 构建 cache-go，用规范仓库的黑盒运行器跑协议层全部用例；
// 操作集本身由 caching 包的向量测试覆盖，适配器 + 插件的 CacheStoreConformance 在 XiHan.Framework 里跑。
func TestCachePassesTheConformanceSuite(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	directory := t.TempDir()
	executable := filepath.Join(directory, "cache-go"+binarySuffix())
	run(t, ctx, nil, "go", "build", "-o", executable, "./cmd/cache-go")
	if err := os.WriteFile(filepath.Join(directory, "xihan.manifest.json"), cache.Manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	version := strings.TrimSpace(run(t, ctx, nil, "go", "list", "-m", "-f", "{{.Version}}", conformanceModule))
	gobin := t.TempDir()
	run(t, ctx, []string{"GOBIN=" + gobin}, "go", "install", conformanceModule+"/cmd/xihan-conformance@"+version)
	runner := filepath.Join(gobin, "xihan-conformance"+binarySuffix())

	command := exec.CommandContext(ctx, runner, "plugin", "--dir", directory, "--json", "--quiet",
		"--config", `{"defaultTimeToLiveSeconds":60,"maxEntries":1000}`,
		"--sample", `cache-entries.set={"key":"k","value":"aGk="}`,
		"--sample", `cache-entries.try-add={"key":"lock","value":"dA=="}`,
		"--sample", `cache-entries.try-refresh={"key":"lock","expected":"dA==","ttlMs":30000}`,
		"--sample", `cache-entries.try-remove={"key":"lock","expected":"dA=="}`,
		"--sample", `cache-entries.get={"key":"k"}`,
		"--sample", `cache-entries.remove={"key":"k"}`)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	var results []result
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("runner output is not JSON (%v; run: %v)\n%s\n%s", err, runErr, stdout.String(), stderr.String())
	}
	if len(results) == 0 {
		t.Fatalf("no results (run: %v)\n%s", runErr, stderr.String())
	}
	// 运行器给出的用例序列必须恰好是它的 AllCases：少跑、多跑或换了次序都算失败
	ran := make([]string, 0, len(results))
	for _, r := range results {
		ran = append(ran, r.Case)
	}
	if all := runnerCases(t, ctx, runner); !slices.Equal(ran, all) {
		t.Errorf("the runner ran %v, want every case of AllCases in order: %v", ran, all)
	}
	skipped := map[string]bool{}
	for _, r := range results {
		// 运行器把跳过也记为 Passed，所以先看 Skipped：只有 expectedSkips 里的用例可以跳过
		switch {
		case r.Skipped && expectedSkips[r.Case] != "":
			skipped[r.Case] = true
			t.Logf("%s: skipped as expected (%s): %s", r.Case, expectedSkips[r.Case], r.Message)
		case r.Skipped:
			t.Errorf("%s: skipped: %s", r.Case, r.Message)
		case !r.Passed:
			t.Errorf("%s: %s", r.Case, r.Message)
		default:
			t.Logf("%s: passed in %dms", r.Case, r.DurationMs)
		}
	}
	for name := range expectedSkips {
		if !skipped[name] {
			t.Errorf("%s: expected to be skipped but was not; update expectedSkips", name)
		}
	}
}

// expectedSkips 是 cache-go 按运行器的规则应当跳过的用例及其理由；其余用例跳过即失败。
var expectedSkips = map[string]string{
	"event":          "cache-go declares no subscription, so the runner has nothing to deliver",
	"event-envelope": "cache-go declares no subscription and no operation that echoes the delivered envelopes",
}

// runnerCases 返回运行器的全部用例名，即一致性模块 plugin 包导出的 AllCases，按执行顺序。
// 测试不能直接引用 AllCases：那个包链接运行器自带的协议生成代码，与本 SDK 的 plugin/v1 注册同一个 plugin.proto，
// 进同一个二进制就在初始化时 panic；运行器的用法说明（help）以 "cases: " 开头的一行逐项列出 AllCases，从这里读。
func runnerCases(t *testing.T, ctx context.Context, runner string) []string {
	t.Helper()
	command := exec.CommandContext(ctx, runner, "help")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("%s help: %v\n%s", runner, err, output.String())
	}
	for line := range strings.Lines(output.String()) {
		if list, ok := strings.CutPrefix(strings.TrimSpace(line), "cases: "); ok {
			return strings.Split(list, ", ")
		}
	}
	t.Fatalf("the usage of the runner does not list its cases:\n%s", output.String())
	return nil
}

// run 执行一个命令并返回 stdout；失败即终止测试。
func run(t *testing.T, ctx context.Context, extraEnv []string, name string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

// binarySuffix 返回可执行文件后缀。
func binarySuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
