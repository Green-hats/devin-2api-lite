// Package connect 实现 Devin 使用的 Connect 协议最小子集。
//
// 服务端流式调用使用 application/connect+proto 与 5 字节长度前缀帧；
// 一元调用使用 application/proto 与裸消息体。错误统一解析为
// “code: message” 形式，交给上层映射 HTTP 状态码。
package connect

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// flagCompressed 表示该帧负载经过压缩。
	flagCompressed = 0x01
	// flagEndStream 表示该帧是流结束信封。
	flagEndStream = 0x02
	// maxMessageSize 限制单帧或一元响应的大小。
	maxMessageSize = 64 << 20
	// headerSize 是流式帧的长度前缀大小。
	headerSize = 5
)

// Error 是 Connect 错误信封或 HTTP 错误体解析出的上游错误。
type Error struct {
	Code    string
	Message string
}

// Error 返回与 connect-go 一致的 “code: message” 文本，便于上层按前缀映射状态码。
func (e *Error) Error() string {
	switch {
	case e.Code == "":
		return e.Message
	case e.Message == "":
		return e.Code
	default:
		return e.Code + ": " + e.Message
	}
}

// Client 是绑定到单个上游地址的 Connect 客户端。
type Client struct {
	baseURL string
	client  *http.Client
}

// NewClient 创建 Connect 客户端；client 需自行处理鉴权与连接策略。
func NewClient(baseURL string, client *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

// ServerStream 打开一次服务端流式调用。
func (c *Client) ServerStream(ctx context.Context, procedure string, payload []byte) (*ServerStream, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+procedure, bytes.NewReader(frame(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/connect+proto")
	request.Header.Set("Accept", "application/connect+proto")
	request.Header.Set("Connect-Protocol-Version", "1")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		return nil, responseError(response)
	}
	return &ServerStream{
		body:   response.Body,
		reader: bufio.NewReaderSize(response.Body, 64<<10),
	}, nil
}

// Unary 执行一次一元调用并返回裸响应消息。
func (c *Client) Unary(ctx context.Context, procedure string, payload []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+procedure, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/proto")
	request.Header.Set("Accept", "application/proto")
	request.Header.Set("Connect-Protocol-Version", "1")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMessageSize))
	if err != nil {
		return nil, fmt.Errorf("read unary response: %w", err)
	}
	return body, nil
}

// ServerStream 是一次已建立的 Connect 服务端流。
type ServerStream struct {
	body   io.ReadCloser
	reader *bufio.Reader
	err    error
	done   bool
}

// Receive 读取下一个消息帧；流结束或出错时返回 false。
func (s *ServerStream) Receive() ([]byte, bool) {
	for !s.done && s.err == nil {
		flags, payload, err := readFrame(s.reader)
		if err != nil {
			s.err = err
			return nil, false
		}
		if flags&flagEndStream != 0 {
			s.done = true
			s.err = endStreamError(payload)
			return nil, false
		}
		if flags&flagCompressed != 0 {
			payload, err = decompress(payload)
			if err != nil {
				s.err = fmt.Errorf("decompress connect frame: %w", err)
				return nil, false
			}
		}
		return payload, true
	}
	return nil, false
}

// Err 返回流结束后的错误；正常结束时为 nil。
func (s *ServerStream) Err() error {
	return s.err
}

// Close 关闭底层响应体。
func (s *ServerStream) Close() error {
	return s.body.Close()
}

// frame 把消息体包成 Connect 流式帧。
func frame(payload []byte) []byte {
	framed := make([]byte, headerSize+len(payload))
	binary.BigEndian.PutUint32(framed[1:headerSize], uint32(len(payload)))
	copy(framed[headerSize:], payload)
	return framed
}

// readFrame 从流中读取一帧的 flags 与负载。
func readFrame(reader *bufio.Reader) (byte, []byte, error) {
	var header [headerSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, nil, errors.New("connect stream ended without an end-stream frame")
		}
		return 0, nil, fmt.Errorf("read connect frame header: %w", err)
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxMessageSize {
		return 0, nil, fmt.Errorf("connect frame too large: %d bytes", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, fmt.Errorf("read connect frame payload: %w", err)
	}
	return header[0], payload, nil
}

// endStreamError 解析流结束信封中的 error 字段。
func endStreamError(payload []byte) error {
	var envelope struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode connect end-stream frame: %w", err)
	}
	if envelope.Error == nil {
		return nil
	}
	return &Error{Code: envelope.Error.Code, Message: envelope.Error.Message}
}

// responseError 把非 200 响应转换为上游错误。
func responseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if len(body) > 0 && json.Unmarshal(body, &envelope) == nil && (envelope.Code != "" || envelope.Message != "") {
		return &Error{Code: envelope.Code, Message: envelope.Message}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return &Error{Code: fmt.Sprintf("http_%d", response.StatusCode), Message: message}
}

// decompress 解压带压缩标志的帧负载。
func decompress(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, maxMessageSize))
}
