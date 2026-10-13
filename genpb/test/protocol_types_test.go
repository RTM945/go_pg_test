package testpb

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	commonpb "go_pg_test/genpb/common"
)

// typeIdOf 从 message 的 descriptor 里把 (proto.type_id) 反射取出来。
// protoc-gen-go 不会为 message option 生成任何 Go 标识符，值只留在
// rawDesc 里，运行时只能这么取。
func typeIdOf(t *testing.T, m protoreflect.ProtoMessage) int32 {
	t.Helper()

	opts := m.ProtoReflect().Descriptor().Options()
	id, ok := proto.GetExtension(opts, commonpb.E_TypeId).(int32)
	if !ok {
		t.Fatalf("%s: (proto.type_id) 没标注或不是 int32", m.ProtoReflect().Descriptor().FullName())
	}
	return id
}

// TestProtocolTypeConstantsMatchDescriptor 校验 protocol_types.go 里的常量
// 和 .proto 里 (proto.type_id) 的标注一致。改了 .proto 忘了跑
// go run ./cmd/genpb 会被这条测出来。
func TestProtocolTypeConstantsMatchDescriptor(t *testing.T) {
	tests := []struct {
		msg  protoreflect.ProtoMessage
		want int32
	}{
		{&CRedEnvelope{}, PROTOCOL_TYPE_CRedEnvelope},
		{&SRedEnvelope{}, PROTOCOL_TYPE_SRedEnvelope},
	}
	for _, tt := range tests {
		name := tt.msg.ProtoReflect().Descriptor().Name()
		t.Run(string(name), func(t *testing.T) {
			if got := typeIdOf(t, tt.msg); got != tt.want {
				t.Errorf("descriptor type_id = %d, PROTOCOL_TYPE_%s = %d", got, name, tt.want)
			}
		})
	}
}
