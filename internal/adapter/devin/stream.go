// 本文件实现上游 Connect 帧到中间响应事件的流式消费。
package devin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Green-hats/devin-2api-lite/internal/adapter"
	"github.com/Green-hats/devin-2api-lite/internal/debuglog"
	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// frameStream 描述 responseStream 消费上游流所需的最小能力。
type frameStream interface {
	// Receive 前进到下一帧，并报告是否成功取得消息。
	Receive() ([]byte, bool)
	// Err 返回流结束时的错误；正常结束返回 nil。
	Err() error
	// Close 释放底层连接；可重复调用。
	Close() error
}

// responseStream 从 Connect 上游按需读取帧并依次返回 decoder 生成的事件。
type responseStream struct {
	// upstream 是 Devin Connect 返回的服务端流。
	upstream frameStream
	// decoder 将一个 Devin protobuf 帧转换为零个或多个中间响应事件。
	decoder *responseDecoder
	// recorder 记录 Devin 原始响应帧；nil 表示禁用调试日志。
	recorder *debuglog.Recorder
	// idleTimeout 是两次帧之间的最长等待时间；0 表示不限制。
	idleTimeout time.Duration
	// readErr 记录空闲超时等本地读取错误，优先于上游错误。
	readErr error
	// closeOnce 保证底层流只关闭一次。
	closeOnce sync.Once
	// closeErr 是首次关闭上游时的错误。
	closeErr error
	// started 表示是否已经请求 decoder 产生 start 事件。
	started bool
	// finished 表示 decoder 已经生成最终事件，不再读取上游。
	finished bool
	// queue 保存已经转换、等待调用方读取的中间响应事件。
	queue []llm.ResponseEvent
}

func (stream *responseStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	for len(stream.queue) == 0 && !stream.finished {
		if err := ctx.Err(); err != nil {
			return llm.ResponseEvent{}, err
		}
		if !stream.started {
			stream.started = true
			stream.queue = append(stream.queue, stream.decoder.start()...)
			break
		}
		payload, ok := stream.receive(ctx)
		if !ok {
			if err := ctx.Err(); err != nil {
				return llm.ResponseEvent{}, err
			}
			readErr := stream.readErr
			if readErr == nil {
				readErr = stream.upstream.Err()
			}
			stream.queue = append(stream.queue, stream.decoder.finish(readErr)...)
			stream.finished = true
			break
		}
		response, err := decodeChatResponse(payload)
		if err != nil {
			stream.queue = append(stream.queue, stream.decoder.fail(fmt.Errorf("decode Devin frame: %w", err))...)
			stream.finished = true
			break
		}
		recordResponse(stream.recorder, response)
		stream.queue = append(stream.queue, stream.decoder.decode(response)...)
		stream.finished = stream.decoder.finished
	}
	if len(stream.queue) > 0 {
		event := stream.queue[0]
		stream.queue = stream.queue[1:]
		return event, nil
	}
	return llm.ResponseEvent{}, io.EOF
}

// receive 在 idleTimeout 内等待下一帧；超时会关闭上游并返回 false。
func (stream *responseStream) receive(ctx context.Context) ([]byte, bool) {
	if stream.idleTimeout <= 0 {
		return stream.upstream.Receive()
	}
	type frameResult struct {
		payload []byte
		ok      bool
	}
	results := make(chan frameResult, 1)
	go func() {
		payload, ok := stream.upstream.Receive()
		results <- frameResult{payload: payload, ok: ok}
	}()
	timer := time.NewTimer(stream.idleTimeout)
	defer timer.Stop()
	select {
	case result := <-results:
		return result.payload, result.ok
	case <-ctx.Done():
		_ = stream.Close()
		<-results
		return nil, false
	case <-timer.C:
		stream.readErr = fmt.Errorf("%w after %s", adapter.ErrStreamIdle, stream.idleTimeout)
		_ = stream.Close()
		<-results
		return nil, false
	}
}

// Close 关闭上游流；可重复调用。
func (stream *responseStream) Close() error {
	if stream == nil {
		return nil
	}
	stream.closeOnce.Do(func() {
		if stream.upstream != nil {
			stream.closeErr = stream.upstream.Close()
		}
	})
	return stream.closeErr
}

// errorCode 把上游/本地错误归一为稳定错误码，供 HTTP 层映射。
func errorCode(err error) string {
	var coded adapter.CodedError
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	switch {
	case errors.Is(err, adapter.ErrStreamIdle):
		return "deadline_exceeded"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, adapter.ErrUnavailable):
		return "unavailable"
	default:
		return ""
	}
}
