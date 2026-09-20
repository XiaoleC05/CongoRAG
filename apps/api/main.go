// Command api 是 ConGoRAG 的对外服务进程。
//
// 这个文件只做两件事：声明 go:embed（embed 指令只能嵌同目录及以下，
// 所以它必须写在这里），然后调用 app.Run。
// 不要往这个文件里加东西，装配在 internal/app/app.go。
package main

import (
	"embed"
	"log"

	"github.com/XiaoleC05/CongoRAG/apps/api/internal/app"
)

//go:embed all:web
var webFS embed.FS

func main() {
	if err := app.Run(webFS); err != nil {
		log.Fatal(err)
	}
}
