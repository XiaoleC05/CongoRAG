#!/bin/sh
# ConGoRAG 交付形态的迁移入口（issue #73 / #74）。
#
# 【它是什么】Dockerfile 的 migrate 这个 target 的 ENTRYPOINT。在启动包里
# 它是一个一次性容器：
#
#   postgres（healthy） → migrate（必须退出码 0） → api / worker
#
# 那个顺序由 deployments/startup/docker-compose.yml 的
# depends_on.condition 保证，不靠这里 sleep。
#
# 【为什么迁移不能放在 api 进程里自己做】两个理由：
#   1. api 和 worker 都要等迁移完成，放在 api 里的结果是 worker 先起来、
#      撞上还不存在的表（river_job），然后开始刷错误日志；
#   2. 升级时"备份成功才迁移"这条顺序（issue #74）需要一个**独立可寻址的
#      步骤**——`docker compose run --rm migrate` 就是它。挤在 api 的启动
#      路径里的话，升级脚本只能靠"起 api 然后祈祷"。
#
# 【为什么不存在"迁移失败时跳过"的分支】set -e 让任何一条失败即以非零退出，
# compose 的 service_completed_successfully 因此不会满足，api / worker 一个都
# 不会启动。这是刻意的：迁移失败还继续起服务，等于让应用对着半截 schema 跑。
#
# 【两条命令都要跑，缺一不可】业务表由 golang-migrate 管（migrations/ 目录，
# 这个镜像里在 /migrations），River 自己的队列表由 river CLI 管——river 的
# 迁移文件嵌在它自己的二进制里，所以那一行没有路径参数。两套系统互相不知道
# 对方存在（见 Makefile 里同名的注释）。少跑一套的表现是 worker 起来后一直报
# `relation "river_job" does not exist`。
#
# 【重复执行是安全的】两条命令都是幂等的：golang-migrate 无事可做时打印
# "no change" 并退出 0（本机实测），river 打印 "no migrations to apply" 同样退出 0。
# 所以这个容器每次 `docker compose up` 都会跑，不会因为"已经迁过了"而红。
set -eu

# 【必须显式检查】migrate 拿到空 DB URL 会以一条难以理解的驱动错误收场，
# 而真实原因是 compose 少传了一个环境变量。
: "${CONGORAG_DB_URL:?环境变量 CONGORAG_DB_URL 没有设——migrate 服务必须拿到它}"

echo "· 业务表迁移（golang-migrate，/migrations）"
migrate -path /migrations -database "$CONGORAG_DB_URL" up

echo "· River 队列表迁移（river CLI，迁移文件在它的二进制里）"
river migrate-up --database-url "$CONGORAG_DB_URL" --line main

echo "✓ 两套迁移都跑完了"
