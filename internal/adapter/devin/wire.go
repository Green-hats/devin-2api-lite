// 本文件实现本适配器所需的最小子集 protobuf wire 编解码。
//
// 上游 63 个 descriptor 生成的绑定体积很大，且需要 protoc 才能重新生成；
// 这里只保留 GetChatMessage / GetCascadeModelConfigs 实际读写的字段，
// 字段编号见 docs/upstream-protocol.md（从上游描述符提取）。
package devin

import (
	"encoding/binary"
	"fmt"
	"math"
)

// protobuf wire type 常量。
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// maxFrameMessageSize 限制单条解码消息的大小，避免畸形长度导致巨额分配。
const maxFrameMessageSize = 64 << 20

// buffer 按 wire 格式累积字段；零值字段直接省略。
type buffer struct {
	data []byte
}

// varint 写入一个 base-128 变长整数。
func (b *buffer) varint(value uint64) {
	for value >= 0x80 {
		b.data = append(b.data, byte(value)|0x80)
		value >>= 7
	}
	b.data = append(b.data, byte(value))
}

// tag 写入字段号与 wire type。
func (b *buffer) tag(field, wire int) {
	b.varint(uint64(field)<<3 | uint64(wire))
}

// str 写入可选字符串字段；空值省略。
func (b *buffer) str(field int, value string) {
	if value == "" {
		return
	}
	b.tag(field, wireBytes)
	b.varint(uint64(len(value)))
	b.data = append(b.data, value...)
}

// boolean 写入可选布尔字段；false 省略（语义上等价于缺失）。
func (b *buffer) boolean(field int, value bool) {
	if !value {
		return
	}
	b.tag(field, wireVarint)
	b.varint(1)
}

// uint 写入可选无符号整数字段；0 省略。
func (b *buffer) uint(field int, value uint64) {
	if value == 0 {
		return
	}
	b.tag(field, wireVarint)
	b.varint(value)
}

// double 写入可选双精度字段；0 省略。
func (b *buffer) double(field int, value float64) {
	if value == 0 {
		return
	}
	b.tag(field, wireFixed64)
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(value))
	b.data = append(b.data, encoded[:]...)
}

// message 写入嵌套消息字段。
func (b *buffer) message(field int, encode func(*buffer)) {
	var nested buffer
	encode(&nested)
	if len(nested.data) == 0 {
		return
	}
	b.tag(field, wireBytes)
	b.varint(uint64(len(nested.data)))
	b.data = append(b.data, nested.data...)
}

// reader 顺序读取 wire 字段；任何畸形输入都会记录错误并停止迭代。
type reader struct {
	data []byte
	pos  int
	wire int
	err  error
}

// newReader 创建一个字段读取器。
func newReader(data []byte) *reader {
	return &reader{data: data}
}

// fail 记录首个解码错误并终止后续读取。
func (r *reader) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
	r.pos = len(r.data)
}

// next 前进到下一个字段并返回字段号；数据耗尽或出错时返回 false。
func (r *reader) next() (int, bool) {
	if r.err != nil || r.pos >= len(r.data) {
		return 0, false
	}
	tag, size := binary.Uvarint(r.data[r.pos:])
	if size <= 0 {
		r.fail("invalid protobuf field tag")
		return 0, false
	}
	r.pos += size
	field := int(tag >> 3)
	r.wire = int(tag & 7)
	if field <= 0 {
		r.fail("invalid protobuf field number %d", field)
		return 0, false
	}
	return field, true
}

// bytes 读取长度前缀字段；wire type 不符时跳过并返回 false。
func (r *reader) bytes() ([]byte, bool) {
	length, ok := r.length()
	if !ok {
		return nil, false
	}
	if length > uint64(len(r.data)-r.pos) {
		r.fail("truncated protobuf field")
		return nil, false
	}
	value := r.data[r.pos : r.pos+int(length)]
	r.pos += int(length)
	return value, true
}

// length 校验 wire type 并读取长度前缀。
func (r *reader) length() (uint64, bool) {
	if r.wire != wireBytes {
		r.skip()
		return 0, false
	}
	length, size := binary.Uvarint(r.data[r.pos:])
	if size <= 0 {
		r.fail("invalid protobuf length prefix")
		return 0, false
	}
	r.pos += size
	if length > maxFrameMessageSize {
		r.fail("protobuf field too large: %d bytes", length)
		return 0, false
	}
	return length, true
}

// str 读取字符串字段。
func (r *reader) str() (string, bool) {
	value, ok := r.bytes()
	return string(value), ok
}

// u64 读取 varint 字段。
func (r *reader) u64() (uint64, bool) {
	if r.wire != wireVarint {
		r.skip()
		return 0, false
	}
	value, size := binary.Uvarint(r.data[r.pos:])
	if size <= 0 {
		r.fail("invalid protobuf varint")
		return 0, false
	}
	r.pos += size
	return value, true
}

// i64 读取有符号 varint 字段（本适配器只用到非负时间戳）。
func (r *reader) i64() (int64, bool) {
	value, ok := r.u64()
	return int64(value), ok
}

// f64 读取双精度字段。
func (r *reader) f64() (float64, bool) {
	if r.wire != wireFixed64 {
		r.skip()
		return 0, false
	}
	if len(r.data)-r.pos < 8 {
		r.fail("truncated protobuf fixed64")
		return 0, false
	}
	value := math.Float64frombits(binary.LittleEndian.Uint64(r.data[r.pos:]))
	r.pos += 8
	return value, true
}

// skip 跳过当前字段值，保证未知字段不会中断解析。
func (r *reader) skip() {
	switch r.wire {
	case wireVarint:
		_, size := binary.Uvarint(r.data[r.pos:])
		if size <= 0 {
			r.fail("invalid protobuf varint")
			return
		}
		r.pos += size
	case wireFixed64:
		if len(r.data)-r.pos < 8 {
			r.fail("truncated protobuf fixed64")
			return
		}
		r.pos += 8
	case wireBytes:
		length, size := binary.Uvarint(r.data[r.pos:])
		if size <= 0 {
			r.fail("invalid protobuf length prefix")
			return
		}
		r.pos += size
		if length > uint64(len(r.data)-r.pos) {
			r.fail("truncated protobuf field")
			return
		}
		r.pos += int(length)
	case wireFixed32:
		if len(r.data)-r.pos < 4 {
			r.fail("truncated protobuf fixed32")
			return
		}
		r.pos += 4
	default:
		r.fail("unsupported protobuf wire type %d", r.wire)
	}
}

// err 返回读取过程中记录的错误。
func (r *reader) errOrNil() error {
	if r.err == nil {
		return nil
	}
	return r.err
}
