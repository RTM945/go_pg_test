# 一个 app 一个目录：app/<APP>/，用 APP=xxx 切目标。
APP ?= test

# 本地同时开着 GoLand 的 run 时 8080 会被占，用 make run ADDR=:8081 换一个。
ADDR ?= :8080

.PHONY: default gen gen-proto gen-schema build run test

default: gen

# proto 全量生成。输出目录不在这里指定——每个 proto 的 go_package 自己决定
# 落到哪个包，加新 app 只要在 proto/ 下开目录、写好 go_package，不用改这个文件。
gen-proto:
	go run ./cmd/genpb

# 表代码和增量 DDL。两个生成器登记在 go.mod 的 tool 里，版本跟着 pdbgen 的 replace 走，
# 不用另外编 exe。genOutput="./ptable" 相对 pdb.xml 所在的目录，在仓库根跑就行。
# ddl 要连库：对照库里现有的结构打印要执行的语句（包在一个事务里），检查后手工执行。
gen-schema:
	go tool bean -schema app/$(APP)/pdb.xml
	go tool ddl -schema app/$(APP)/pdb.xml

gen: gen-proto gen-schema

build:
	go build -o bin/$(APP).exe ./app/$(APP)

# pdb.xml 的位置只有这里知道，代码里默认按"二进制旁边放配置"找
run:
	go run ./app/$(APP) -schema app/$(APP)/pdb.xml -addr $(ADDR)

test:
	go test ./...
