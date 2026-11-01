# 应用pdbgen的业务模板
自带了 Login 逻辑，和一个领红包的例子（今天的次数每天 5 点清零）。

## 生成代码
```
make gen-proto    # proto → genpb/ 和处理函数注册表（go run ./cmd/genpb）
make gen-schema   # pdb.xml → app/test/ptable/<表名>.pdb.go，再打印要执行的增量 DDL（要连库，检查后手工执行）
```
表代码和 DDL 的生成器是 pdbgen 的 `gen/bean`、`gen/ddl`，登记在 go.mod 的 `tool` 里，跟着 `replace pdbgen => ../pdbgen` 走，
不用另外编 exe：`go tool bean -schema app/test/pdb.xml`、`go tool ddl -schema app/test/pdb.xml`。

pdb.xml 里每张表都写了 `lock`：改一行（`SetXxx` / `Delete`）要持有那个玩家的 uid 锁
（`db.WithUserLock` / `db.WithUserTryLock`，`session.User` 的处理函数已经拿着），否则 panic。

## 跑起来
```
make run                                  # 默认 :8080，要先起好下面的 PostgreSQL、PgBouncer、Redis
go run ./cmd/robot                        # 登录 → 心跳 → 拉事件 → 领红包 → 重放拒绝 → 免登录 → 顶号
go run ./cmd/robot -addr http://127.0.0.1:8081
```

# 最新最潮的wlsc
```
# 1. 拉取镜像
wslc pull postgres:14.24-alpine3.23
wslc pull edoburu/pgbouncer

# 2. 创建并启动 PostgreSQL
wslc run --name my-postgres -e POSTGRES_PASSWORD=mysecretpassword -d postgres:14.24-alpine3.23

# 3. 创建 app 用户
wslc exec my-postgres psql -U postgres -c "CREATE USER app WITH PASSWORD 'app';"

# 4. 创建数据库
wslc exec my-postgres psql -U postgres -c "CREATE DATABASE gamedb OWNER app;"

# 用app user登录创建表 所有权属于app 这样应用就不会报权限问题
wslc exec -it my-postgres psql -U app -d gamedb

# 解决第三方sql工具无法连接pgBouncer的问题
CREATE SCHEMA IF NOT EXISTS pgbouncer;
CREATE OR REPLACE FUNCTION pgbouncer.get_auth(p_usename text)
RETURNS TABLE(usename name, passwd text) AS $$
  SELECT usename, passwd FROM pg_catalog.pg_shadow WHERE usename = p_usename;
$$ LANGUAGE sql SECURITY DEFINER;

GRANT USAGE ON SCHEMA pgbouncer TO app;
GRANT EXECUTE ON FUNCTION pgbouncer.get_auth(text) TO app;


wslc network create pgnet
wslc network connect pgnet my-postgres

# 启动 PgBouncer
wslc run -d `
  --name my-pgbouncer `
  --network pgnet `
  -p 6432:6432 `
  -e 'DATABASE_URL=postgres://app:app@my-postgres:5432/gamedb' `
  -e 'LISTEN_PORT=6432' `
  -e 'AUTH_TYPE=scram-sha-256' `
  -e 'AUTH_USER=app' `
  -e 'AUTH_QUERY=SELECT * FROM pgbouncer.get_auth($1)' `
  -e 'POOL_MODE=transaction' `
  -e 'DEFAULT_POOL_SIZE=20' `
  -e 'MAX_CLIENT_CONN=100' `
  edoburu/pgbouncer:latest

# redis  
wslc run -d `
  --name my-redis `
  -p 6379:6379 `
  redis:alpine `
  redis-server --requirepass 123456
```