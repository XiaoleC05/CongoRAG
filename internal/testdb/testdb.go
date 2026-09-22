// Package testdb 给"需要真实 PostgreSQL 的集成测试"提供数据库。
//
// 它只被测试文件 import，所以不会进任何生产二进制（api / worker 都不引用它）。
//
// ── 为什么要有这个包（issue #70）────────────────────────────────
//
// 在此之前，"测试库从哪来"是**环境约定**而不是代码：本机靠
// `make test-integration`（需要开发机的 compose 里那个 Postgres 在跑），
// CI 靠 workflow 里的 service container，两者靠 `CONGORAG_TEST_DB_URL`
// 这一个变量对齐。约定对齐得再好，它也是"两边各自准备一套、
// 出了问题才知道不一样"。
//
// 这个包把那个约定写成代码：**它保证返回时 schema 已经就绪**，
// 两条路径给的是同一个保证。
//
// ── 两条路径 ─────────────────────────────────────────────────
//
//  1. `CONGORAG_TEST_DB_URL` 已设置（CI 与 `make test-integration` 用的）：
//     连它，**不动 schema**，只**校验** schema 在不在。谁设的变量谁负责
//     准备好它——CI 那一步是显式的 migration job step，Makefile 那条会
//     重建库再跑迁移。校验而不是重跑，是因为对着一个外部库执行迁移会
//     复制一份 golang-migrate 的版本簿记，而那份簿记已经有一个实现。
//     校验失败时报的错会写清"该跑哪条命令"，而不是让测试用一堆
//     "relation does not exist" 去猜。
//
//  2. 没设、但设了 `CONGORAG_TESTCONTAINERS=1`：**自己起一个容器**
//     （testcontainers-go），灌完 schema 再返回。镜像与
//     `deployments/docker/docker-compose.yml` 用同一个常量，避免
//     "测试库与开发库版本不同"这类假绿。`make test-integration` 走这条，
//     于是本机验证不再依赖开发机上跑着什么、也不再需要手工跑两套迁移。
//
//  3. 两个都没设：跳过。
//
// ── 为什么容器那条要一个独立的开关，而不是"没设 URL 就起容器" ──────
// 那会让 `go test ./...`（以及 CI 里那个不连库的 go job）默认开始拉镜像、
// 起容器——把一个离线、秒级、不需要 Docker 的动作变成联网、几十秒、
// 依赖 Docker 的动作。这条包不该改变"默认什么也不做"。
package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgresImage 是集成测试用的镜像。
//
// 【必须和 deployments/docker/docker-compose.yml 一致】两边版本不同的话，
// 测试里绿的 SQL 可能在开发库上炸（或者反过来），而那种失败最费时间：
// 它看起来像代码问题。CI 的 service container 也用的是这个 tag。
//
// 【为什么是 pgvector 镜像】migrations/0001 的第一句就是
// CREATE EXTENSION IF NOT EXISTS vector，官方 postgres 镜像没有这个扩展。
const PostgresImage = "pgvector/pgvector:0.8.6-pg18"

// 两个门控变量。
const (
	// urlEnv 指向一个**已经迁移好**的库。CI 的 integration job 用它
	// （迁移是那一步里显式的一行，看得见）。
	urlEnv = "CONGORAG_TEST_DB_URL"

	// containersEnv 让测试自己起一个容器。`make test-integration` 用它。
	// 独立于 urlEnv 的理由见包注释。
	containersEnv = "CONGORAG_TESTCONTAINERS"
)

// userMessage 是两条跳过路径共用的开头，避免两句话各说一半。
const userMessage = "需要真实 PostgreSQL 的集成测试没有可用的库："

// 连接这一个容器的超时。镜像首次拉取会慢，所以给得比一般的宽。
const (
	containerStartTimeout = 3 * time.Minute
	pingTimeout           = 30 * time.Second
	// schemaReadyTimeout 是"等库能接受连接"的上限——健康检查过了之后
	// 还要等一下初始化脚本收尾，这是我们自己的重试，与容器的
	// WaitStrategy 互补。
	schemaReadyTimeout = 60 * time.Second
)

// Require 返回一个 schema 已经就绪的连接池；没法提供时跳过测试。
func Require(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	if dbURL := os.Getenv(urlEnv); dbURL != "" {
		pool, err := pgxpool.New(ctx, dbURL)
		if err != nil {
			t.Fatalf("连不上 %s 指向的数据库：%v", urlEnv, err)
		}
		t.Cleanup(pool.Close)
		waitForReady(t, pool, func() {
			t.Fatalf("%s 指向的库 schema 不完整——\n"+
				"它该由设置这个变量的人准备好（CI 是 migration step；"+
				"本机可以直接用 make test-integration，它走容器那条路）", urlEnv)
		})
		return pool
	}

	if os.Getenv(containersEnv) != "1" {
		t.Skip(userMessage + "设 " + urlEnv + " 指向一个已迁移的库，或者设 " +
			containersEnv + "=1 让测试自己起一个容器（make test-integration 就是后者）")
	}

	dbURL, terminate, ok := startContainer(t)
	if !ok {
		t.Skip(userMessage + containersEnv + "=1 已设置，但起不了 Docker 容器")
	}
	t.Cleanup(terminate)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("连不上测试容器：%v", err)
	}
	t.Cleanup(pool.Close)

	// 【灌 schema 之前先确认连得上】等待策略已经等到第二次 ready 了，
	// 但"日志打出来了"和"TCP 真的能握手"之间还有一个很短的窗口。
	// 这里等的是**连接**而不是脚本成功——脚本不是幂等的（CREATE TABLE
	// 没写 IF NOT EXISTS），重跑一次会在"已经建过"上失败，
	// 那时分不清是重跑还是真的坏了。
	waitForConnect(t, pool)

	if err := applySchema(ctx, pool); err != nil {
		t.Fatalf("往测试容器里灌 schema 失败：%v", err)
	}
	waitForReady(t, pool, func() { t.Fatal("测试容器的 schema 灌完之后仍然不可用") })
	return pool
}

// waitForConnect 等到这个池真的能跑通一次查询。
func waitForConnect(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), schemaReadyTimeout)
	defer cancel()

	for {
		pingCtx, cancelPing := context.WithTimeout(ctx, pingTimeout)
		err := pool.Ping(pingCtx)
		cancelPing()
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("测试容器起来了但连不上：%v", err)
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// startContainer 起一个 PostgreSQL 容器并返回它的连接串。
//
// 第二个返回值是清理函数（终止容器），第三个是"起没起来"——起不了
// （没有 Docker、镜像拉不动）时返回 false 让调用方跳过测试，
// 而不是让每一个集成测试都红成一片。本机没装 Docker 是常见情形，
// 而它不该被当成测试失败。
func startContainer(t *testing.T) (string, func(), bool) {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        PostgresImage,
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "postgres",
			"POSTGRES_PASSWORD": "postgres",
			"POSTGRES_DB":       "congorag",
		},
		// 【等待策略必须比"端口开了"更强，而且要等**第二次**那句 ready】
		//
		// postgres 官方镜像的启动分两段：先起一个只监听 unix socket 的临时
		// 实例跑初始化脚本，关掉它，再用正式的配置（含 TCP）起第二次。
		// 等待策略写 `ForLog(...)` 不带 occurrence 的话，会在**第一段**就
		// 匹配上，然后立刻去连——那时临时实例正在关闭，连接以
		// "unexpected EOF" 失败。实测踩过这一次。
		//
		// 所以是「第二次 ready」+「端口在监听」两个条件同时满足。
		WaitingFor: wait.ForAll(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2),
			wait.ForListeningPort("5432/tcp"),
		).WithStartupTimeout(containerStartTimeout),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Logf("起不了测试容器（本机 Docker 没在跑？）：%v", err)
		return "", nil, false
	}

	terminate := func() {
		// 【用 Background 而不是 t.Context()】清理发生在这个测试结束之后，
		// 那一刻测试的 ctx 已经取消了，用它终止容器会失败并留下一个
		// 占着端口的孤儿容器。
		_ = container.Terminate(context.Background())
	}

	host, err := container.Host(ctx)
	if err != nil {
		terminate()
		t.Logf("拿不到容器地址：%v", err)
		return "", nil, false
	}
	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		terminate()
		t.Logf("拿不到容器映射端口：%v", err)
		return "", nil, false
	}

	return fmt.Sprintf("postgres://postgres:postgres@%s:%s/congorag?sslmode=disable",
		host, port.Port()), terminate, true
}

// applySchema 往一个**全新的**库上灌当前 schema。
//
// ── 为什么直接执行迁移文件，而不是引 golang-migrate 当库 ─────────
// 这个库保证是空的（容器刚起来），所以"按文件名排序、逐个执行"与
// golang-migrate 在一个空库上的行为完全一致——没有版本簿记要读，
// 也没有半途失败要继续的语义。为这一件事再引一个依赖（还要捎上它的
// 驱动）不划算。
//
// 【什么时候这条捷径会失效】有人开始依赖 dirty 库的恢复、或者
// 迁移里出现需要 golang-migrate 特有的多语句事务边界时。
// 那时应该改成引 golang-migrate 的库，而不是在这里加判断。
func applySchema(ctx context.Context, pool *pgxpool.Pool) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(filepath.Join(root, "migrations"))
	if err != nil {
		return fmt.Errorf("读迁移目录：%w", err)
	}

	var ups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)

	for _, name := range ups {
		body, err := os.ReadFile(filepath.Join(root, "migrations", name))
		if err != nil {
			return fmt.Errorf("读 %s：%w", name, err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("执行 %s：%w", name, err)
		}
	}

	// River 自己的队列表是另一套（技术方案 §二：两套迁移系统并存），
	// 走它自己的 Go API 而不是那条 CLI——CLI 不一定装在跑测试的机器上，
	// 而这个 API 就在我们已经依赖的 river 模块里。
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("构造 river 迁移器：%w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("执行 river 迁移：%w", err)
	}
	return nil
}

// waitForReady 等到库既连得上、schema 也在。
//
// 【为什么要探 schema 而不只是 Ping】这条包的存在意义就是"返回时 schema
// 已经就绪"；只 Ping 的话，一个迁移没跑完的库会顺利通过，
// 然后每一条测试各自以 "relation does not exist" 失败——
// 那正是这个包想消掉的那种失败。
func waitForReady(t *testing.T, pool *pgxpool.Pool, fail func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), schemaReadyTimeout)
	defer cancel()

	for {
		err := probeSchema(ctx, pool)
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Logf("schema 探测最后一次失败：%v", err)
			fail()
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// probeSchema 摸两张分别属于两套迁移系统的表。
//
// 【两张都要摸】只查业务表的话，"River 迁移没跑"会被漏过去，
// 而 platform 的 scheduler 集成测试需要的正是 river_job。
func probeSchema(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	row := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM knowledge_bases),
		  (SELECT count(*) FROM river_job)`)
	var kbs, jobs int
	if err := row.Scan(&kbs, &jobs); err != nil {
		return err
	}
	return nil
}

// repoRoot 从本文件的位置推回仓库根。
//
// 【为什么不能用相对路径 "../.." 】测试的工作目录是**它自己所在的包目录**
// （internal/knowledge、internal/llm、internal/platform……），深度不一致，
// 相对路径要在每个包里各写一遍。用 runtime.Caller 拿到本文件的绝对路径
// 就与调用方无关了。
func repoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("拿不到 testdb 包自身的路径")
	}
	// internal/testdb/testdb.go → internal/testdb → internal → 仓库根
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..")), nil
}

// 编译期断言：pgx 的 Row 接口在这里被用到（probeSchema 的 Scan）。
var _ = pgx.Row(nil)
