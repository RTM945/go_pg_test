# 一个 app 一个目录：app/<APP>/，用 APP=xxx 切目标。
APP ?= test

# 本地同时开着 GoLand 的 run 时 8080 会被占，用 make run ADDR=:8081 换一个。
ADDR ?= :8080

# 调 bin/ 下的 exe 要用 $(CURDIR)/bin 的绝对路径，别写 ../../bin。
# Windows 上 make 用的是 cmd.exe，而 cmd 里 / 是开关分隔符不是路径分隔符，
# 以 .. 开头的相对路径会被当成命令名，报 "'..' 不是内部或外部命令"。
# 绝对路径 + 正斜杠在 cmd 和 sh 里都认。
BIN := $(CURDIR)/bin

.PHONY: default gen gen-proto gen-schema build run test

default: gen

# proto 全量生成。输出目录不在这里指定——每个 proto 的 go_package 自己决定
# 落到哪个包，加新 app 只要在 proto/ 下开目录、写好 go_package，不用改这个文件。
gen-proto:
	go run ./cmd/genpb

# 表代码。genOutput="./ptable" 是相对 cwd 的，必须 cd 进 app 目录再跑。
gen-schema:
	cd app/$(APP) && $(BIN)/genbeans.exe -schema pdb.xml
	cd app/$(APP) && $(BIN)/genddl.exe -schema pdb.xml

gen: gen-proto gen-schema

build:
	go build -o bin/$(APP).exe ./app/$(APP)

# pdb.xml 的位置只有这里知道，代码里默认按"二进制旁边放配置"找
run:
	go run ./app/$(APP) -schema app/$(APP)/pdb.xml -addr $(ADDR)

test:
	go test ./...
