// client 给跑着的服务器发协议，把响应解出来打给人看，实机调试用。
//
// 走 descriptor 驱动：读 genpb/desc.pb 拿全部 message 和 type_id，所以加新协议
// 不用改这个工具。
//
//	go run ./cmd/client -msg CRedEnvelopeClick -data '{"actId":42}'
//	go run ./cmd/client -type 123
//	go run ./cmd/client -msg CRedEnvelope -raw 082a
//	go run ./cmd/client -list
package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	gopgio "go_pg_test/io"

	_ "go_pg_test/genpb/common" // 注册 (proto.type_id) 扩展，下面要靠它读协议号
	commonpb "go_pg_test/genpb/common"
)

var (
	addr = flag.String("addr", "http://127.0.0.1:8080", "server address")
	name = flag.String("msg", "CRedEnvelope", "protocol name, short or full (CRedEnvelope / test.CRedEnvelope)")
	id   = flag.Int("type", 0, "typeId, alternative to -msg")
	data = flag.String("data", "", "payload as JSON, field names as in the .proto")
	raw  = flag.String("raw", "", "payload as hex, e.g. 082a; for crafting bad frames")
	list = flag.Bool("list", false, "list all protocols and exit")
)

func main() {
	flag.Parse()

	files, err := loadDescriptors()
	if err != nil {
		log.Fatal(err)
	}
	byName, byTypeId := indexMessages(files)

	if *list {
		printAll(byTypeId)
		return
	}

	md, typeId, err := resolve(byName, byTypeId)
	if err != nil {
		log.Fatal(err)
	}

	payload, err := buildPayload(md)
	if err != nil {
		log.Fatal(err)
	}

	frames, status, body, err := roundTrip(typeId, payload)
	if err != nil {
		log.Fatal(err)
	}

	// md 可能是 nil——-type 指定了客户端不认识的协议号
	msgName := "?"
	if md != nil {
		msgName = string(md.Name())
	}
	fmt.Printf("→ %-24s typeId=%-4d %s\n", msgName, typeId, hex.EncodeToString(payload))

	if status != http.StatusOK {
		log.Fatalf("← HTTP %d  %s\n", status, strings.TrimSpace(string(body)))
	}
	if len(frames) == 0 {
		fmt.Println("← 服务器一帧都没回")
		return
	}
	for i, f := range frames {
		printFrame(i, len(frames), f, byTypeId)
	}
}

// loadDescriptors 读 genpb/desc.pb。它由 genpb 每次生成时一并写出，和代码同源。
func loadDescriptors() (*protoregistry.Files, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	repoRoot, err := findRepoRoot(wd)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(filepath.Join(repoRoot, "genpb", "desc.pb"))
	if err != nil {
		return nil, fmt.Errorf("cannot read desc.pb; run go run ./cmd/genpb first: %w", err)
	}

	fds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(raw, fds); err != nil {
		return nil, err
	}
	return protodesc.NewFiles(fds)
}

// indexMessages 建立 短名/全名 → message 和 typeId → message 两张索引。
func indexMessages(files *protoregistry.Files) (map[string]protoreflect.MessageDescriptor, map[int32]protoreflect.MessageDescriptor) {
	byName := map[string]protoreflect.MessageDescriptor{}
	byTypeId := map[int32]protoreflect.MessageDescriptor{}

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			md := msgs.Get(i)
			byName[string(md.Name())] = md
			byName[string(md.FullName())] = md

			if opts, ok := md.Options().(*descriptorpb.MessageOptions); ok && proto.HasExtension(opts, commonpb.E_TypeId) {
				if typeId, ok := proto.GetExtension(opts, commonpb.E_TypeId).(int32); ok {
					byTypeId[typeId] = md
				}
			}
		}
		return true
	})
	return byName, byTypeId
}

func resolve(byName map[string]protoreflect.MessageDescriptor, byTypeId map[int32]protoreflect.MessageDescriptor) (protoreflect.MessageDescriptor, int32, error) {
	if *name == "" && *id == 0 {
		return nil, 0, errors.New("specify a protocol: -msg CRedEnvelope or -type 125; -list shows all")
	}

	if *name != "" {
		md, ok := byName[*name]
		if !ok {
			return nil, 0, fmt.Errorf("no protocol named %q; -list shows all", *name)
		}
		opts, _ := md.Options().(*descriptorpb.MessageOptions)
		typeId, ok := proto.GetExtension(opts, commonpb.E_TypeId).(int32)
		if !ok {
			return nil, 0, fmt.Errorf("%s has no (proto.type_id), cannot send", md.FullName())
		}
		return md, typeId, nil
	}

	md, ok := byTypeId[int32(*id)]
	if !ok {
		// 放行客户端不认识的协议号——这正是在测服务端的"未注册协议号"分支，
		// 是 -type 的正当用法之一。
		fmt.Fprintf(os.Stderr, "client: note: typeId %d is not in desc.pb; sending raw bytes, response shown raw too\n", *id)
	}
	return md, int32(*id), nil
}

func buildPayload(md protoreflect.MessageDescriptor) ([]byte, error) {
	switch {
	case *raw != "":
		return hex.DecodeString(strings.ReplaceAll(*raw, " ", ""))
	case *data != "":
		if md == nil {
			return nil, errors.New("client does not know this typeId; cannot build -data, use -raw")
		}
		m := dynamicpb.NewMessage(md)
		if err := protojson.Unmarshal([]byte(*data), m); err != nil {
			return nil, fmt.Errorf("-data is not valid %s JSON: %w", md.Name(), err)
		}
		return proto.Marshal(m)
	default:
		return nil, nil
	}
}

// roundTrip 发一帧、把响应里的帧全读回来。编码和解码都用 io 包，格式不在本地重写一遍。
// 请求侧是严格单帧，响应侧可能多帧，所以这里用 ReadFrames。
func roundTrip(typeId int32, payload []byte) (frames []gopgio.Frame, status int, body []byte, err error) {
	var frame bytes.Buffer
	if err := gopgio.WriteFrame(&frame, typeId, payload); err != nil {
		return nil, 0, nil, err
	}

	resp, err := http.Post(*addr+"/", "application/octet-stream", &frame)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	status = resp.StatusCode
	if status != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return nil, status, buf.Bytes(), nil
	}

	if frames, err = gopgio.ReadFrames(resp.Body); err != nil {
		return nil, status, nil, err
	}
	return frames, status, nil, nil
}

// printFrame 打一帧。一次响应可能是多帧（服务端把多个模块的响应一起发回来），
// 所以带上 [i/n] 前缀；只有一帧时不加，保持平时的样子。
func printFrame(idx, total int, f gopgio.Frame, byTypeId map[int32]protoreflect.MessageDescriptor) {
	typeId, payload := f.TypeId, f.Payload

	arrow := "←"
	if total > 1 {
		arrow = fmt.Sprintf("← [%d/%d]", idx+1, total)
	}

	md, ok := byTypeId[typeId]
	if !ok {
		fmt.Printf("%s typeId=%-4d no protocol registered, raw %s\n", arrow, typeId, hex.EncodeToString(payload))
		return
	}

	m := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(payload, m); err != nil {
		fmt.Printf("%s %s typeId=%d cannot decode: %v, raw %s\n", arrow, md.Name(), typeId, err, hex.EncodeToString(payload))
		return
	}

	fmt.Printf("%s %-24s typeId=%-4d %d bytes\n", arrow, md.Name(), typeId, len(payload))

	// 用 prototext 不用 protojson：int64 在 JSON 里会被渲染成字符串，时间戳看着别扭
	out, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(m)
	if err != nil {
		fmt.Printf("  %s\n", hex.EncodeToString(payload))
		return
	}
	if body := strings.TrimRight(string(out), "\n"); body != "" {
		for line := range strings.SplitSeq(body, "\n") {
			fmt.Println("  " + tidyLine(line))
		}
	}

	// proto3 不编码零值，字段直接不出现。不点明的话会让人以为字段丢了。
	if missing := missingFields(m); len(missing) > 0 {
		fmt.Printf("  (zero-valued fields not in payload: %s)\n", strings.Join(missing, ", "))
	}
}

// tidyLine 把冒号后的空白压成一格。protobuf 的 prototext 会故意塞随机数量的空格，
// 输出每次构建都不一样，调试时看着烦。
func tidyLine(line string) string {
	if k, v, ok := strings.Cut(line, ":"); ok {
		return k + ": " + strings.TrimSpace(v)
	}
	return line
}

// missingFields 列出值为零、因而没被编码进包里的字段。
func missingFields(m protoreflect.Message) []string {
	fields := m.Descriptor().Fields()
	var missing []string
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !m.Has(fd) {
			missing = append(missing, string(fd.Name()))
		}
	}
	return missing
}

func printAll(byTypeId map[int32]protoreflect.MessageDescriptor) {
	ids := make([]int, 0, len(byTypeId))
	for id := range byTypeId {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		md := byTypeId[int32(id)]
		fmt.Printf("%-6d %-24s %s\n", id, md.Name(), md.FullName())
	}
}

func findRepoRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod found up to the filesystem root")
		}
		dir = parent
	}
}
