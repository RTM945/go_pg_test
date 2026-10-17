package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"

	commonpb "go_pg_test/genpb/common"
)

func TestAppProtoPath(t *testing.T) {
	tests := []struct {
		path     string
		app      string
		logicPkg string
		ok       bool
	}{
		{"test/red_envelope.proto", "test", "red_envelope", true},
		{"lobby/login.proto", "lobby", "login", true},
		{"ext.proto", "", "", false},                                       // 顶层共享 proto 不算 app 协议
		{"test/notproto.txt", "", "", false},                               // 不是 .proto
		{"test/", "", "", false},                                           // 没有文件名
		{"google/protobuf/descriptor.proto", "google", "descriptor", true}, // 靠 go_package 的 module 前缀再滤一层
	}
	for _, tt := range tests {
		app, logicPkg, ok := appProtoPath(tt.path)
		if app != tt.app || logicPkg != tt.logicPkg || ok != tt.ok {
			t.Errorf("appProtoPath(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.path, app, logicPkg, ok, tt.app, tt.logicPkg, tt.ok)
		}
	}
}

func TestCollectAppProtoFiles(t *testing.T) {
	files := []*descriptorpb.FileDescriptorProto{
		{
			Name:        new("test/red_envelope.proto"),
			Package:     new("test"),
			Syntax:      new("proto3"),
			Options:     fileOptions("go_pg_test/genpb/test;testpb"),
			MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("CRedEnvelope", 123)},
		},
		{
			// 顶层共享 proto，没写 go_package 的分号形式也不该被当成 app 协议
			Name:    new("ext.proto"),
			Package: new("proto"),
			Syntax:  new("proto3"),
			Options: fileOptions("go_pg_test/genpb/common;commonpb"),
		},
		{
			// module 之外的（desc.pb 里 --include_imports 带进来的）
			Name:    new("google/protobuf/descriptor.proto"),
			Package: new("google.protobuf"),
			Syntax:  new("proto3"),
			Options: fileOptions("google.golang.org/protobuf/types/descriptorpb"),
		},
	}

	got, err := collectAppProtoFiles(newFiles(t, files...), "go_pg_test", commonpb.E_TypeId)
	if err != nil {
		t.Fatalf("collectAppProtoFiles() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("收集到 %d 个文件，want 1 个", len(got))
	}

	pf := got[0]
	if pf.App != "test" || pf.LogicPackage != "red_envelope" || pf.GoPackage != "testpb" {
		t.Errorf("推导出来的是 %+v", pf)
	}
	if pf.MsgImportPath() != "go_pg_test/app/test/msg" {
		t.Errorf("MsgImportPath() = %q", pf.MsgImportPath())
	}
	if len(pf.ClientMessages()) != 1 || pf.ClientMessages()[0].Name != "CRedEnvelope" {
		t.Errorf("ClientMessages() = %+v", pf.ClientMessages())
	}
}

func stubProtoFile() *protoFile {
	return &protoFile{
		Path:         "test/red_envelope.proto",
		App:          "test",
		LogicPackage: "red_envelope",
		GoImportPath: "go_pg_test/genpb/test",
		GoPackage:    "testpb",
		Module:       "go_pg_test",
		Messages: []*protoMsg{
			{Name: "CRedEnvelope", TypeId: 123},
			{Name: "SRedEnvelope", TypeId: 124},
			{Name: "CRedEnvelopeClick", TypeId: 125},
		},
	}
}

func stubPath(root string) string {
	return filepath.Join(root, "app", "test", "logic", "red_envelope", "msg_red_envelope.go")
}

func TestGenLogicStubCreates(t *testing.T) {
	root := t.TempDir()
	path := stubPath(root)

	f, err := genLogicStub(root, stubProtoFile())
	if err != nil {
		t.Fatalf("genLogicStub() error = %v", err)
	}
	if f == nil {
		t.Fatal("genLogicStub() = nil, want a stub")
	}
	if f.path != path {
		t.Errorf("path = %q, want %q", f.path, path)
	}
	src := string(f.data)

	// 两个 C 协议都要有 stub，S 协议不该有
	for _, want := range []string{"func ProcessCRedEnvelope(", "func ProcessCRedEnvelopeClick("} {
		if !strings.Contains(src, want) {
			t.Errorf("缺 %q\n%s", want, src)
		}
	}
	if strings.Contains(src, "ProcessSRedEnvelope") {
		t.Errorf("服务端协议不该有处理函数\n%s", src)
	}
	if !strings.Contains(src, `"context"`) || !strings.Contains(src, `"go_pg_test/app/test/msg"`) {
		t.Errorf("import 不全\n%s", src)
	}
}

// 补齐时人写的那部分必须一字未动——这是整套生成的安全性根基。
// 生成阶段只把结果放内存，磁盘上的人写文件必须原样不动。
func TestGenLogicStubTopsUpWithoutTouchingHumanCode(t *testing.T) {
	root := t.TempDir()
	path := stubPath(root)
	const human = `package red_envelope

import (
	"context"

	"go_pg_test/app/test/msg"
)

func ProcessCRedEnvelope(ctx context.Context, req *msg.CRedEnvelope) error {
	return realWork()
}
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(human), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := genLogicStub(root, stubProtoFile())
	if err != nil {
		t.Fatalf("genLogicStub() error = %v", err)
	}
	if f == nil {
		t.Fatal("genLogicStub() = nil, want topped-up content")
	}
	src := string(f.data)

	if !strings.Contains(src, human) {
		t.Errorf("人写的那段被改动了\n--- got ---\n%s", src)
	}
	if !strings.Contains(src, "func ProcessCRedEnvelopeClick(") {
		t.Errorf("缺的函数没补上\n%s", src)
	}
	if got := readFile(t, path); got != human {
		t.Errorf("生成阶段就写了磁盘，应该在发布时才写\n%s", got)
	}
}

func TestGenLogicStubIdempotent(t *testing.T) {
	root := t.TempDir()
	path := stubPath(root)

	f, err := genLogicStub(root, stubProtoFile())
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("first genLogicStub() = nil, want a stub")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, f.data, 0o644); err != nil {
		t.Fatal(err)
	}

	// 补过一次之后函数就全在了，第二次应该没有改动可发布
	second, err := genLogicStub(root, stubProtoFile())
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Errorf("第二次跑仍有改动可发布\n%s", second.data)
	}
}

// 文件里没有 context / msg 的导入时不猜，直接报错，且不写坏文件
func TestGenLogicStubRefusesWithoutImports(t *testing.T) {
	root := t.TempDir()
	path := stubPath(root)
	const bare = "package red_envelope\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(bare), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := genLogicStub(root, stubProtoFile())
	if err == nil {
		t.Fatal("genLogicStub() = nil, want error")
	}
	if f != nil {
		t.Error("报错时不该返回内容")
	}
	if !strings.Contains(err.Error(), "missing import") {
		t.Errorf("error = %q, should name the missing import", err)
	}
	if got := readFile(t, path); got != bare {
		t.Errorf("报错了却改了文件\n%s", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 生成物必须能过 gofmt，过不了说明模板坏了
func TestRenderRejectsBadCode(t *testing.T) {
	if _, err := render("package x\n\nfunc {{{ \n", nil); err == nil {
		t.Fatal("render 应该报错")
	}

	src, err := render("package {{.P}}\n", struct{ P string }{"ok"})
	if err != nil {
		t.Fatalf("render 失败: %v", err)
	}
	if !strings.HasPrefix(string(src), "package ok") {
		t.Errorf("render 输出 = %q", src)
	}
}
