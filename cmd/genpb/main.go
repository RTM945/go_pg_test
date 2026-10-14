package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	_ "go_pg_test/genpb/common"
)

func main() {
	// gen proto
	vtFeatures := "marshal+unmarshal+size+pool"

	wd, _ := os.Getwd()
	repoRoot, err := findRepoRoot(wd)
	if err != nil {
		log.Fatal("findRepoRoot:", err)
	}

	module, err := modulePath(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		log.Fatal("modulePath:", err)
	}

	// 清理必须排在 protoc 前面，否则会把自己刚产出的文件删掉
	if err := cleanGenerated(repoRoot); err != nil {
		log.Fatal("cleanGenerated:", err)
	}

	// go_pg_test/proto，所有 proto 共用的 -I 根
	protoRoot := filepath.Join(repoRoot, "proto")
	// 生成物落到 repoRoot 下，落哪个子目录由每个 proto 的 go_package 决定
	// （protoc 的 paths=import + module= 会自己剥掉 module 前缀）
	outRoot := repoRoot
	desc := filepath.Join(repoRoot, "genpb", "desc.pb")
	// protoc include
	protocInclude := filepath.Join(repoRoot, "third_party", "protoc", "include")
	// vt include
	vtInclude := filepath.Join(repoRoot, "third_party", "vtprotobuf", "include")

	protoFiles, err := collectProtoFiles(protoRoot)
	if err != nil {
		log.Fatal("collectProtoFiles:", err)
	}
	if len(protoFiles) == 0 {
		log.Fatal("No .proto files found")
	}

	log.Printf("Found %d proto files\n", len(protoFiles))
	for _, p := range protoFiles {
		log.Println(" -", p)
	}

	gobin, err := goEnv("GOBIN")
	if err != nil {
		log.Printf("go env GOBIN: %v", err)
	}
	if gobin == "" {
		// fallback to GOPATH/bin
		gopath, err := goEnv("GOPATH")
		if err != nil {
			log.Printf("go env GOPATH: %v", err)
		}
		gobin = filepath.Join(gopath, "bin")
	}

	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	pGo := filepath.Join(gobin, "protoc-gen-go"+ext)
	pGrpc := filepath.Join(gobin, "protoc-gen-go-grpc"+ext)
	pVt := filepath.Join(gobin, "protoc-gen-go-vtproto"+ext)

	missing := []struct {
		path   string
		modPkg string
		binary string
	}{
		{pGo, "google.golang.org/protobuf/cmd/protoc-gen-go@latest", "protoc-gen-go"},
		{pGrpc, "google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest", "protoc-gen-go-grpc"},
		{pVt, "github.com/planetscale/vtprotobuf/cmd/protoc-gen-go-vtproto@latest", "protoc-gen-go-vtproto"},
	}

	for i := range missing {
		if !fileExists(missing[i].path) {
			log.Printf("Installing %s ...\n", missing[i].binary)
			if err := goInstall(missing[i].modPkg); err != nil {
				log.Printf("failed to install %s: %v\n", missing[i].binary, err)
				// continue and try PATH later
			} else {
				// recompute from gobin
				missing[i].path = filepath.Join(gobin, missing[i].binary+ext)
			}
		}
	}

	for _, m := range missing {
		if !fileExists(m.path) {
			// try to resolve from PATH
			p, _ := exec.LookPath(m.binary + ext)
			if p == "" {
				log.Printf("warning: plugin %s not found (expected at %s). protoc may fail.\n", m.binary, m.path)
			} else {
				log.Printf("Found %s in PATH: %s\n", m.binary, p)
				// override
				switch m.binary {
				case "protoc-gen-go":
					pGo = p
				case "protoc-gen-go-grpc":
					pGrpc = p
				case "protoc-gen-go-vtproto":
					pVt = p
				}
			}
		}
	}
	// 构建 protoc 参数
	args := []string{
		"-I.", // protoRoot 下为根
		"-I" + protocInclude,
		"-I" + vtInclude,
		"--include_imports",
		"--include_source_info",
	}

	// plugin flags
	if fileExists(pGo) {
		args = append(args, fmt.Sprintf("--plugin=protoc-gen-go=%s", pGo))
	}
	if fileExists(pGrpc) {
		args = append(args, fmt.Sprintf("--plugin=protoc-gen-go-grpc=%s", pGrpc))
	}
	if fileExists(pVt) {
		args = append(args, fmt.Sprintf("--plugin=protoc-gen-go-vtproto=%s", pVt))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var outb, errb bytes.Buffer
	var cmd *exec.Cmd

	// outputs：paths=import + module=，让每个 proto 的 go_package 自己决定落到
	// 哪个 Go 包。module= 和 paths=source_relative 互斥，不能混用。
	args = append(args,
		fmt.Sprintf("--go_out=%s", outRoot),
		fmt.Sprintf("--go_opt=module=%s", module),
		fmt.Sprintf("--go-grpc_out=%s", outRoot),
		fmt.Sprintf("--go-grpc_opt=module=%s", module),
		fmt.Sprintf("--go-vtproto_out=%s", outRoot),
		fmt.Sprintf("--go-vtproto_opt=module=%s", module),
		fmt.Sprintf("--go-vtproto_opt=features=%s", vtFeatures),
	)

	args = append(args, fmt.Sprintf("--descriptor_set_out=%s", desc))

	// append proto files (absolute or relative)
	args = append(args, protoFiles...)
	cmd = exec.CommandContext(ctx, repoRoot+"/bin/protoc", args...)
	cmd.Dir = protoRoot
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		log.Fatalf("*** protoc failed:\nSTDOUT:\n%s\nSTDERR:\n%s\n", outb.String(), errb.String())
	}

	// map<message, typeId>
	descb, err := os.ReadFile(desc)
	if err != nil {
		log.Fatal(err)
	}
	fds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(descb, fds); err != nil {
		log.Fatal(err)
	}
	files, err := protodesc.NewFiles(fds)
	if err != nil {
		log.Fatal(err)
	}

	var typeIDExtType protoreflect.ExtensionType
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		exts := fd.Extensions()
		for i := 0; i < exts.Len(); i++ {
			ext := exts.Get(i)
			if string(ext.Name()) == "type_id" {
				typeIDExtType, err = protoregistry.GlobalTypes.FindExtensionByName(ext.FullName())
				if err != nil {
					log.Fatal(err)
				}
				return false
			}
		}
		return true
	})
	if typeIDExtType == nil {
		log.Fatal("proto.type_id extension not found; check that proto/ext.proto exists and has been generated")
	}

	groups, err := collectTypeIds(files, typeIDExtType)
	if err != nil {
		log.Fatal("collectTypeIds:", err)
	}
	for _, g := range groups {
		if err := writeProtocolTypes(repoRoot, module, g); err != nil {
			log.Fatal("writeProtocolTypes:", err)
		}
	}

	// app 侧：msg 包装类、logic 脚手架、注册表
	protos, err := collectAppProtoFiles(files, module, typeIDExtType)
	if err != nil {
		log.Fatal("collectAppProtoFiles:", err)
	}
	if err := genAppCode(repoRoot, module, protos); err != nil {
		log.Fatal("genAppCode:", err)
	}

	fmt.Printf("protoc succeeded. generated files under %s\n", filepath.Join(repoRoot, "genpb"))
}

// cleanGenerated 把上次生成的东西全删掉，重头再来。
//
// 按路径删，不解析文件内容——删哪些是写死的一张单子，改动这里就是改动契约：
//
//	genpb/                            整个目录，protoc 和 genpb 的产物都在这
//	app/<app>/msg/                    协议包装类
//	app/<app>/processor_registry.gen.go
//
// 刻意不碰的：
//
//	app/<app>/logic/                  msg_*.go 里是人写的业务逻辑
//	app/<app>/ptable/                 pdbgen 的产出，不是我们的地盘
//
// 必须先于 protoc 执行，否则会删掉刚产出的东西。
//
// 代价是 protoc 失败时 genpb/ 是空的——重跑一次就好，文件都在版本库里。
func cleanGenerated(repoRoot string) error {
	genpbDir := filepath.Join(repoRoot, "genpb")
	if err := os.RemoveAll(genpbDir); err != nil {
		return err
	}
	log.Printf("cleared %s", genpbDir)

	apps, err := os.ReadDir(filepath.Join(repoRoot, "app"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, app := range apps {
		if !app.IsDir() {
			continue
		}
		appDir := filepath.Join(repoRoot, "app", app.Name())

		if err := os.RemoveAll(filepath.Join(appDir, "msg")); err != nil {
			return err
		}
		registry := filepath.Join(appDir, "processor_registry.gen.go")
		if err := os.Remove(registry); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// 标记必须在 package 之前——Go 的约定，go generate 一类工具靠它在文件开头认生成物，
// 我们自己的 cleanGenerated 也靠它。
const protocolTypesTmpl = `// Code generated by genpb; DO NOT EDIT.

package {{.GoPackage}}

{{range .TypeIds}}
const PROTOCOL_TYPE_{{.Name}} int32 = {{.TypeId}}
{{end}}`

// writeProtocolTypes 把一个 go_package 下的协议号常量写进它自己的包。
// 每个 app 一份，互不污染——app 编译时不会链到别人的协议号。
func writeProtocolTypes(repoRoot, module string, g *typeIdGroup) error {
	rel, ok := strings.CutPrefix(g.GoImportPath, module+"/")
	if !ok {
		return fmt.Errorf("go_package %q is not under module %s", g.GoImportPath, module)
	}
	return writeGenerated(filepath.Join(repoRoot, filepath.FromSlash(rel), "protocol_types.go"), protocolTypesTmpl, g)
}

// modulePath 从 go.mod 里读出 module 名，别在代码里硬编码。
func modulePath(goModPath string) (string, error) {
	f, err := os.Open(goModPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if after, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s: no module line found", goModPath)
}

func goEnv(name string) (string, error) {
	cmd := exec.Command("go", "env", name)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func goInstall(mod string) error {
	// run: go install <mod>
	cmd := exec.Command("go", "install", mod)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func findRepoRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir { // 到达根了
			break
		}
		dir = parent
	}
	return "", errors.New("repo root (go.mod) not found")
}

type msg2typeId struct {
	Name   string // message 名，用来生成 PROTOCOL_TYPE_<Name>
	TypeId int32
	where  string // 声明位置，只用于报错
}

// typeIdGroup 一个 go_package 下所有带 (proto.type_id) 的 message，
// 对应要生成的一份 protocol_types.go。
type typeIdGroup struct {
	GoImportPath string // go_pg_test/genpb/test
	GoPackage    string // testpb
	TypeIds      []*msg2typeId
}

// collectTypeIds 收集所有打了 (proto.type_id) 的 message，按 typeId 升序返回。
//
// type_id 撞车和 message 重名都是致命的，这里直接报错而不是放过去：
//   - type_id 撞车：两个协议共用一个编号，收包时永远只走其中一个，另一个的
//     常量还会被静默吞掉，全程没有任何提示。
//   - message 重名：生成的 PROTOCOL_TYPE_<Name> 会重复，编译器在生成文件里
//     报 redeclared，看不出是谁和谁撞了。
func collectTypeIds(files *protoregistry.Files, typeIDExtType protoreflect.ExtensionType) ([]*typeIdGroup, error) {
	byTypeId := make(map[int32]*msg2typeId)
	byName := make(map[string]*msg2typeId)
	byPackage := make(map[string]*typeIdGroup)
	var fail error

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		// desc.pb 里带着 --include_imports 拉进来的 google/protobuf/*.proto，
		// 它们的 go_package 不符合本仓库约定，所以只在真碰到带 type_id 的
		// message 时才去解析，别的文件一律不管。
		var g *typeIdGroup

		messages := fd.Messages()
		for i := 0; i < messages.Len(); i++ {
			msg := messages.Get(i)
			opts := msg.Options()
			if opts == nil || !proto.HasExtension(opts, typeIDExtType) {
				continue
			}

			if g == nil {
				goImportPath, goPackage, err := goPackageOf(fd)
				if err != nil {
					fail = err
					return false
				}
				g = byPackage[goImportPath]
				if g == nil {
					g = &typeIdGroup{GoImportPath: goImportPath, GoPackage: goPackage}
					byPackage[goImportPath] = g
				}
			}

			typeId := proto.GetExtension(opts, typeIDExtType).(int32)
			name := string(msg.Name())
			where := fmt.Sprintf("%s (%s)", msg.FullName(), fd.Path())

			// type_id 全局唯一，跨 app 也不许重复
			if prev, ok := byTypeId[typeId]; ok {
				fail = fmt.Errorf("duplicate type_id %d: %s and %s", typeId, prev.where, where)
				return false
			}
			if prev, ok := byName[name]; ok {
				fail = fmt.Errorf("duplicate message name %s: %s and %s would both generate PROTOCOL_TYPE_%s", name, prev.where, where, name)
				return false
			}

			m := &msg2typeId{Name: name, TypeId: typeId, where: where}
			byTypeId[typeId] = m
			byName[name] = m
			g.TypeIds = append(g.TypeIds, m)
		}
		return true
	})
	if fail != nil {
		return nil, fail
	}

	groups := make([]*typeIdGroup, 0, len(byPackage))
	for _, g := range byPackage {
		slices.SortStableFunc(g.TypeIds, func(i, j *msg2typeId) int {
			return cmp.Compare(i.TypeId, j.TypeId)
		})
		groups = append(groups, g)
	}
	slices.SortStableFunc(groups, func(i, j *typeIdGroup) int {
		return cmp.Compare(i.GoImportPath, j.GoImportPath)
	})
	return groups, nil
}

// goPackageOf 拆出 proto 文件 go_package 里的 import 路径和包名。
// 必须写成 "go_pg_test/genpb/test;testpb" 这种带分号的形式，
// 否则 import 路径推导出来的包名不一定是我们想要的那个。
func goPackageOf(fd protoreflect.FileDescriptor) (importPath, packageName string, err error) {
	opts, ok := fd.Options().(*descriptorpb.FileOptions)
	if !ok || opts.GetGoPackage() == "" {
		return "", "", fmt.Errorf("%s: no go_package", fd.Path())
	}
	importPath, packageName, ok = strings.Cut(opts.GetGoPackage(), ";")
	if !ok || importPath == "" || packageName == "" {
		return "", "", fmt.Errorf(`%s: go_package must be "import/path;pkgname", got %q`,
			fd.Path(), opts.GetGoPackage())
	}
	return importPath, packageName, nil
}

func collectProtoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && filepath.Ext(path) == ".proto" {
			rel, err := filepath.Rel(root, path) // 相对于 protoRoot 的相对路径
			if err != nil {
				return err
			}
			// 使用 Unix 风格分隔符更可靠（protoc 接受 /）
			rel = filepath.ToSlash(rel)
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}
