package logging

import (
	"log/slog"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	commonpb "go_pg_test/genpb/common"
)

const redacted = "***"

// ProtoValue 把 protobuf 消息转成日志值：打了 (proto.sensitive) 的字段只打成 "***"
// （map 字段只隐去 value）。不改原消息。genpb 生成的消息包装类的 LogValue 调它。
func ProtoValue(m proto.Message) slog.Value {
	c := proto.Clone(m)
	redact(c.ProtoReflect())
	return slog.StringValue(prototext.MarshalOptions{}.Format(c))
}

func redact(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case proto.GetExtension(fd.Options(), commonpb.E_Sensitive).(bool):
			redactField(m, fd, v)
		case fd.IsMap():
			if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					redact(mv.Message())
					return true
				})
			}
		case fd.IsList():
			if fd.Message() != nil {
				for i := range v.List().Len() {
					redact(v.List().Get(i).Message())
				}
			}
		case fd.Message() != nil:
			redact(v.Message())
		}
		return true
	})
}

func redactField(m protoreflect.Message, fd protoreflect.FieldDescriptor, v protoreflect.Value) {
	switch {
	case fd.IsMap() && fd.MapValue().Kind() == protoreflect.StringKind:
		v.Map().Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
			v.Map().Set(k, protoreflect.ValueOfString(redacted))
			return true
		})
	case !fd.IsMap() && !fd.IsList() && fd.Kind() == protoreflect.StringKind:
		m.Set(fd, protoreflect.ValueOfString(redacted))
	default:
		m.Clear(fd) // 其他类型没法写成 "***"，整个去掉
	}
}
