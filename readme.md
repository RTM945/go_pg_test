# 应用pdbgen的业务模板
自带了Login逻辑

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