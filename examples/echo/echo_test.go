package echo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xihanfun/framework-sdk-go/examples/echo"
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

// TestEchoPassesTheConformanceSuite 构建 echo-go，把它和清单放进临时目录，用规范仓库的黑盒运行器 xihan-conformance 跑全部用例。
// 运行器以独立进程运行：它自带一份协议生成代码，与本 SDK 的 plugin/v1 不能链接进同一个二进制。
func TestEchoPassesTheConformanceSuite(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	directory := t.TempDir()
	executable := filepath.Join(directory, "echo-go"+binarySuffix())
	run(t, ctx, nil, "go", "build", "-o", executable, "./cmd/echo-go")
	if err := os.WriteFile(filepath.Join(directory, "xihan.manifest.json"), echo.Manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	version := strings.TrimSpace(run(t, ctx, nil, "go", "list", "-m", "-f", "{{.Version}}", conformanceModule))
	gobin := t.TempDir()
	run(t, ctx, []string{"GOBIN=" + gobin}, "go", "install", conformanceModule+"/cmd/xihan-conformance@"+version)
	runner := filepath.Join(gobin, "xihan-conformance"+binarySuffix())

	command := exec.CommandContext(ctx, runner, "plugin", "--dir", directory, "--json", "--quiet",
		"--config", `{"greeting":"hello"}`,
		"--sample", `echoes.create={"text":"conformance"}`,
		"--exclude", "echoes.crash")
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
	for _, r := range results {
		if !r.Passed {
			t.Errorf("%s: %s", r.Case, r.Message)
		} else {
			t.Logf("%s: passed in %dms (skipped=%v)", r.Case, r.DurationMs, r.Skipped)
		}
	}
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
