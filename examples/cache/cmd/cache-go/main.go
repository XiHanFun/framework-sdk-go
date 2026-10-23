// cache-go 是远程缓存 Provider 示例插件的可执行入口：把它和 xihan.manifest.json 放进同一目录，宿主即可加载。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/xihanfun/framework-sdk-go/examples/cache"
)

func main() {
	if err := cache.Serve(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
