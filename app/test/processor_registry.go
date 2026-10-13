package main

// 人工注册的协议放这里。客户端协议（C 开头）由 genpb 自动注册到
// processor_registry.gen.go，不要抄一份到这个文件。

var processRegistry = NewProcessorRegistry()

func init() {
	// GM / RPC 协议在这里注册，例如：
	//	processRegistry.Register(somepb.PROTOCOL_TYPE_XXX, msg.NewXXX, xxx.ProcessXXX, processQuery)
}
