// 本文件提供尚未配置具体供应商时使用的适配器占位实现。
package adapter

import (
	"context"
	"fmt"

	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// Unavailable 是尚未配置具体供应商时使用的占位适配器。
type Unavailable struct {
	// Reason 是返回给调用方的未配置原因。
	Reason string
}

// err 返回可被 errors.Is(adapter.ErrUnavailable) 识别的错误。
func (unavailable Unavailable) err() error {
	if unavailable.Reason == "" {
		return ErrUnavailable
	}
	return fmt.Errorf("%w: %s", ErrUnavailable, unavailable.Reason)
}

// Stream 返回未配置供应商适配器的错误。
func (unavailable Unavailable) Stream(context.Context, llm.RequestMessages) (llm.ResponseStream, error) {
	return nil, unavailable.err()
}

// ListModels 返回未配置供应商适配器的错误。
func (unavailable Unavailable) ListModels(context.Context) ([]ModelInfo, error) {
	return nil, unavailable.err()
}
