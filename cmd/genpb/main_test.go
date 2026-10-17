package main

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	commonpb "go_pg_test/genpb/common"
)

func fileOptions(goPackage string) *descriptorpb.FileOptions {
	return &descriptorpb.FileOptions{GoPackage: new(goPackage)}
}

func msgWithTypeId(name string, typeId int32) *descriptorpb.DescriptorProto {
	opts := &descriptorpb.MessageOptions{}
	proto.SetExtension(opts, commonpb.E_TypeId, typeId)
	return &descriptorpb.DescriptorProto{Name: new(name), Options: opts}
}

func newFiles(t *testing.T, fds ...*descriptorpb.FileDescriptorProto) *protoregistry.Files {
	t.Helper()
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: fds})
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v", err)
	}
	return files
}

// loadDescriptors 必须能在没有任何生成代码的情况下工作：生成器一旦依赖
// genpb/common，生成物缺失时自己就编不过。
func TestLoadDescriptorsWithoutGeneratedCode(t *testing.T) {
	// ext.proto: extend google.protobuf.MessageOptions { int32 type_id = 51234; }
	extProto := &descriptorpb.FileDescriptorProto{
		Name:       new("ext.proto"),
		Package:    new("proto"),
		Syntax:     new("proto3"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		Extension: []*descriptorpb.FieldDescriptorProto{{
			Name:     new("type_id"),
			Number:   new(int32(51234)),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
			Extendee: new(".google.protobuf.MessageOptions"),
		}},
	}

	// type_id 以 unknown fields 的形式写进 options，等价于扩展未注册时的解析结果。
	opts := &descriptorpb.MessageOptions{}
	raw := protowire.AppendTag(nil, 51234, protowire.VarintType)
	opts.ProtoReflect().SetUnknown(protowire.AppendVarint(raw, 101))

	testProto := &descriptorpb.FileDescriptorProto{
		Name:       new("test/a.proto"),
		Package:    new("test"),
		Syntax:     new("proto3"),
		Dependency: []string{"ext.proto"},
		Options:    fileOptions("go_pg_test/genpb/test;testpb"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    new("Msg"),
			Options: opts,
		}},
	}

	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto),
		extProto,
		testProto,
	}}
	rawSet, err := proto.Marshal(fds)
	if err != nil {
		t.Fatal(err)
	}

	files, extType, err := loadDescriptors(rawSet)
	if err != nil {
		t.Fatalf("loadDescriptors() error = %v", err)
	}
	fd, err := files.FindFileByPath("test/a.proto")
	if err != nil {
		t.Fatal(err)
	}
	msgOpts := fd.Messages().ByName("Msg").Options()
	if !hasTypeId(msgOpts, extType) {
		t.Fatal("hasTypeId() = false, want true")
	}
	if got := proto.GetExtension(msgOpts, extType).(int32); got != 101 {
		t.Errorf("type_id = %d, want 101", got)
	}
}

func TestCollectTypeIds(t *testing.T) {
	fd := &descriptorpb.FileDescriptorProto{
		Name:    new("test/red_envelop.proto"),
		Package: new("test"),
		Syntax:  new("proto3"),
		Options: fileOptions("go_pg_test/genpb/test;testpb"),
		MessageType: []*descriptorpb.DescriptorProto{
			msgWithTypeId("MsgB", 124),
			msgWithTypeId("MsgA", 123),
			{Name: new("NoTypeId")},
		},
	}

	got, err := collectTypeIds(newFiles(t, fd), commonpb.E_TypeId)
	if err != nil {
		t.Fatalf("collectTypeIds() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("分了 %d 个包，want 1 个: %+v", len(got), got)
	}

	g := got[0]
	if g.GoImportPath != "go_pg_test/genpb/test" || g.GoPackage != "testpb" {
		t.Errorf("包信息 = {%s, %s}, want {go_pg_test/genpb/test, testpb}", g.GoImportPath, g.GoPackage)
	}

	// 没打 (proto.type_id) 的跳过，剩下的按 typeId 升序
	want := []struct {
		name   string
		typeId int32
	}{{"MsgA", 123}, {"MsgB", 124}}

	if len(g.TypeIds) != len(want) {
		t.Fatalf("收集到 %d 个，want %d 个: %+v", len(g.TypeIds), len(want), g.TypeIds)
	}
	for i, w := range want {
		if g.TypeIds[i].Name != w.name || g.TypeIds[i].TypeId != w.typeId {
			t.Errorf("TypeIds[%d] = {%s, %d}, want {%s, %d}", i, g.TypeIds[i].Name, g.TypeIds[i].TypeId, w.name, w.typeId)
		}
	}
}

// 每个 go_package 一组，协议号常量各写各的包。
func TestCollectTypeIdsGroupsByPackage(t *testing.T) {
	files := []*descriptorpb.FileDescriptorProto{
		{
			Name:        new("bbb/b.proto"),
			Package:     new("bbb"),
			Syntax:      new("proto3"),
			Options:     fileOptions("go_pg_test/genpb/bbb;bbbbp"),
			MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgB", 200)},
		},
		{
			Name:        new("aaa/a.proto"),
			Package:     new("aaa"),
			Syntax:      new("proto3"),
			Options:     fileOptions("go_pg_test/genpb/aaa;aaapb"),
			MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgA", 100)},
		},
	}

	got, err := collectTypeIds(newFiles(t, files...), commonpb.E_TypeId)
	if err != nil {
		t.Fatalf("collectTypeIds() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("分了 %d 个包，want 2 个", len(got))
	}
	// 按 import path 排序
	if got[0].GoPackage != "aaapb" || got[1].GoPackage != "bbbbp" {
		t.Errorf("分组顺序 = {%s, %s}, want {aaapb, bbbbp}", got[0].GoPackage, got[1].GoPackage)
	}
}

func TestCollectTypeIdsError(t *testing.T) {
	tests := []struct {
		name    string
		files   []*descriptorpb.FileDescriptorProto
		wantMsg string
	}{
		{
			name: "同一文件里 type_id 撞车",
			files: []*descriptorpb.FileDescriptorProto{{
				Name:        new("a.proto"),
				Package:     new("a"),
				Syntax:      new("proto3"),
				Options:     fileOptions("go_pg_test/genpb/a;apb"),
				MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgA", 123), msgWithTypeId("MsgB", 123)},
			}},
			wantMsg: "duplicate type_id 123",
		},
		{
			name: "跨文件 type_id 撞车",
			files: []*descriptorpb.FileDescriptorProto{
				{
					Name:        new("a.proto"),
					Package:     new("a"),
					Syntax:      new("proto3"),
					Options:     fileOptions("go_pg_test/genpb/a;apb"),
					MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgA", 123)},
				},
				{
					Name:        new("b.proto"),
					Package:     new("b"),
					Syntax:      new("proto3"),
					Options:     fileOptions("go_pg_test/genpb/b;bpb"),
					MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgB", 123)},
				},
			},
			wantMsg: "duplicate type_id 123",
		},
		{
			// 同包重名 protodesc 自己就拦了（already declared），
			// 跨包同名 proto 是合法的，但会生成两个 PROTOCOL_TYPE_MsgA
			name: "跨包 message 重名",
			files: []*descriptorpb.FileDescriptorProto{
				{
					Name:        new("a.proto"),
					Package:     new("a"),
					Syntax:      new("proto3"),
					Options:     fileOptions("go_pg_test/genpb/a;apb"),
					MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgA", 123)},
				},
				{
					Name:        new("b.proto"),
					Package:     new("b"),
					Syntax:      new("proto3"),
					Options:     fileOptions("go_pg_test/genpb/b;bpb"),
					MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgA", 124)},
				},
			},
			wantMsg: "duplicate message name MsgA",
		},
		{
			name: "没写 go_package",
			files: []*descriptorpb.FileDescriptorProto{{
				Name:        new("a.proto"),
				Package:     new("a"),
				Syntax:      new("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgA", 123)},
			}},
			wantMsg: "no go_package",
		},
		{
			name: "go_package 缺包名",
			files: []*descriptorpb.FileDescriptorProto{{
				Name:        new("a.proto"),
				Package:     new("a"),
				Syntax:      new("proto3"),
				Options:     fileOptions("go_pg_test/genpb/a"),
				MessageType: []*descriptorpb.DescriptorProto{msgWithTypeId("MsgA", 123)},
			}},
			wantMsg: "import/path;pkgname",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := collectTypeIds(newFiles(t, tt.files...), commonpb.E_TypeId)
			if err == nil {
				t.Fatal("collectTypeIds() = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want 包含 %q", err, tt.wantMsg)
			}
			t.Logf("拦下来了: %v", err)
		})
	}
}
