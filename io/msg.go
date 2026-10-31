package io

// Msg 是一条协议消息。protoc-gen-go 生成的类型，加上 vtprotobuf 的编解码方法和
// genpb 生成的 TypeId（genpb/<app>/protocol_types.go），本身就满足它，不用再包一层。
type Msg interface {
	// TypeId 是协议号，写在帧头。
	TypeId() int32
	SizeVT() int
	// MarshalToSizedBufferVT 从 dAtA 的末尾往前写，len(dAtA) 必须正好是 SizeVT()。
	MarshalToSizedBufferVT(dAtA []byte) (int, error)
	UnmarshalVT(dAtA []byte) error
}
