// Command worker 是 ConGoRAG 的异步任务消费端。
//
// 和 apps/api/main.go 同一个理由——这个文件只做一件事：调用装配根。
// 不要往这里加东西，装配在 internal/app/app.go。
//
// 【这个进程没有 go:embed】它不提供 HTTP，也不内嵌前端。
package main

import (
	"log"

	"github.com/XiaoleC05/CongoRAG/apps/worker/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
