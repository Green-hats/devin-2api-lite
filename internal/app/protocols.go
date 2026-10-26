// 本文件定义两种 API 协议在 app 层的统一适配边界。
package app

import (
	"fmt"

	"github.com/Green-hats/devin-2api-lite/internal/api/openai/chat"
	"github.com/Green-hats/devin-2api-lite/internal/api/openai/responses"
	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// protocol 把一个协议的请求解码与响应编码绑定在同一个值上，
// 避免调用方把 A 协议的解码器和 B 协议的编码器配在一起。
type protocol interface {
	// decode 把协议 JSON 解码为供应商无关的请求上下文。
	decode(data []byte) (llm.RequestMessages, protocolOptions, error)
	// NewStreamEncoder 创建与本次 HTTP 请求绑定的流式编码器。
	NewStreamEncoder(model string, includeUsage bool) streamEncoder
	// EncodeFinal 把最终助手消息编码为完整的非流式 JSON 响应体。
	EncodeFinal(message *llm.AssistantMessage) ([]byte, error)
	// SSEFormat 把单个 SSE 事件格式化为可写入客户端的字节。
	SSEFormat(name string, data []byte) []byte
}

// streamEncoder 抽象两种协议共有的中间事件编码。
type streamEncoder interface {
	Encode(event llm.ResponseEvent) ([]eventWire, error)
}

type eventWire struct {
	Name string
	Data []byte
}

// protocolOptions 保存两个协议都需要的生成控制选项。
type protocolOptions struct {
	Stream       bool
	IncludeUsage bool
}

// responsesProtocol 实现 OpenAI Responses API 协议。
type responsesProtocol struct{}

func (p responsesProtocol) decode(data []byte) (llm.RequestMessages, protocolOptions, error) {
	adapted, err := responses.DecodeRequest(data)
	if err != nil {
		return llm.RequestMessages{}, protocolOptions{}, err
	}
	return adapted.Context, protocolOptions{
		Stream:       adapted.Options.Stream,
		IncludeUsage: false,
	}, nil
}

func (p responsesProtocol) NewStreamEncoder(model string, _ bool) streamEncoder {
	return &responsesStreamAdapter{responses.NewStreamEncoder(model)}
}

func (p responsesProtocol) EncodeFinal(message *llm.AssistantMessage) ([]byte, error) {
	return responses.EncodeResponse(message)
}

func (p responsesProtocol) SSEFormat(name string, data []byte) []byte {
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", name, data)
}

// chatProtocol 实现 OpenAI Chat Completions 协议。
type chatProtocol struct{}

func (p chatProtocol) decode(data []byte) (llm.RequestMessages, protocolOptions, error) {
	adapted, err := chat.DecodeRequest(data)
	if err != nil {
		return llm.RequestMessages{}, protocolOptions{}, err
	}
	return adapted.Context, protocolOptions{
		Stream:       adapted.Options.Stream,
		IncludeUsage: adapted.Options.IncludeUsage,
	}, nil
}

func (p chatProtocol) NewStreamEncoder(model string, includeUsage bool) streamEncoder {
	return &chatStreamAdapter{chat.NewStreamEncoder(model, includeUsage)}
}

func (p chatProtocol) EncodeFinal(message *llm.AssistantMessage) ([]byte, error) {
	return chat.EncodeResponse(message)
}

func (p chatProtocol) SSEFormat(name string, data []byte) []byte {
	// OpenAI Chat Completions 使用 data-only SSE；[DONE] 作为流终止标记。
	if name == "[DONE]" {
		return []byte("data: [DONE]\n\n")
	}
	return fmt.Appendf(nil, "data: %s\n\n", data)
}

type responsesStreamAdapter struct{ encoder *responses.StreamEncoder }

func (a *responsesStreamAdapter) Encode(event llm.ResponseEvent) ([]eventWire, error) {
	raw, err := a.encoder.Encode(event)
	if err != nil {
		return nil, err
	}
	out := make([]eventWire, 0, len(raw))
	for _, e := range raw {
		out = append(out, eventWire{Name: e.Name, Data: e.Data})
	}
	return out, nil
}

type chatStreamAdapter struct{ encoder *chat.StreamEncoder }

func (a *chatStreamAdapter) Encode(event llm.ResponseEvent) ([]eventWire, error) {
	raw, err := a.encoder.Encode(event)
	if err != nil {
		return nil, err
	}
	out := make([]eventWire, 0, len(raw))
	for _, e := range raw {
		out = append(out, eventWire{Name: e.Name, Data: e.Data})
	}
	return out, nil
}
