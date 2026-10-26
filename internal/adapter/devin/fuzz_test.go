// 本文件对 wire 解码器做模糊测试，保证任意上游字节都不会触发 panic。
package devin

import "testing"

// FuzzDecodeChatResponse 覆盖手写 protobuf 解码的全部路径。
func FuzzDecodeChatResponse(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x1a, 0x02, 'h', 'i'})
	f.Add([]byte{0x28, 0x0a})
	f.Add([]byte{0x32, 0x05, 0x0a, 0x03, 'c', 'a', 'l'})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = decodeChatResponse(data)
	})
}
