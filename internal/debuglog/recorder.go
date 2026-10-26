// 本文件实现单次 HTTP 请求的分阶段调试日志目录。
//
// Package debuglog 负责记录兼容 API 请求在 HTTP、中间模型和供应商协议之间的转换过程。
// lite 版只保留最小能力：每请求一个目录，写入 JSON/JSONL，便于排查
// “HTTP ⇄ 中间模型 ⇄ 上游”任意一跳的问题。
package debuglog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Manager 在固定 logs 根目录下为每次请求创建独立 recorder。
type Manager struct {
	// root 是所有请求日志目录的根路径；空值表示禁用调试日志。
	root string
	// now 返回当前时间；测试会固定它以验证同秒目录分配。
	now func() time.Time
	// mutex 保证同一秒并发请求的目录后缀分配不会冲突。
	mutex sync.Mutex
}

// RequestMeta 是创建请求日志时已经确定的 HTTP 元信息。
type RequestMeta struct {
	// Method 是 HTTP 请求方法。
	Method string
	// Path 是 HTTP 请求路径。
	Path string
}

// Completion 是请求结束时写入 meta.json 的结果摘要。
type Completion struct {
	// StatusCode 是最终 HTTP 状态码。
	StatusCode int
	// Result 是 completed、failed 或 disconnected。
	Result string
	// Model 是请求使用的模型标识。
	Model string
	// Provider 是实际生成响应的供应商标识。
	Provider string
	// Stream 表示请求是否使用流式响应。
	Stream bool
}

// Recorder 保存单次请求的目录与开始时间；nil 接收者上的方法均为空操作。
type Recorder struct {
	// directory 是本次请求的日志目录。
	directory string
	// startedAt 是 HTTP 请求进入应用的时间。
	startedAt time.Time
	// requestMeta 保存创建时的 HTTP 元信息。
	requestMeta RequestMeta
	// mutex 串行化同一请求内的文件写入。
	mutex sync.Mutex
}

// NewManager 创建写入指定 logs 根目录的管理器；空路径会返回禁用状态的管理器。
func NewManager(root string) *Manager {
	return &Manager{root: root, now: time.Now}
}

// Start 为一个 HTTP 请求创建按进入秒命名的独立日志目录。
func (manager *Manager) Start(meta RequestMeta) *Recorder {
	if manager == nil || manager.root == "" {
		return nil
	}
	if err := os.MkdirAll(manager.root, 0o700); err != nil {
		return nil
	}
	now := manager.now()
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	base := now.Format("20060102-150405")
	for suffix := 1; ; suffix++ {
		name := base
		if suffix > 1 {
			name = fmt.Sprintf("%s-%02d", base, suffix)
		}
		directory := filepath.Join(manager.root, name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil
		}
		return &Recorder{directory: directory, startedAt: now, requestMeta: meta}
	}
}

// Complete 写入请求结果摘要。
func (recorder *Recorder) Complete(completion Completion) {
	if recorder == nil {
		return
	}
	recorder.WriteJSON("meta.json", map[string]any{
		"status_code": completion.StatusCode,
		"result":      completion.Result,
		"model":       completion.Model,
		"provider":    completion.Provider,
		"stream":      completion.Stream,
		"duration_ms": time.Since(recorder.startedAt).Milliseconds(),
		"method":      recorder.requestMeta.Method,
		"path":        recorder.requestMeta.Path,
	})
}

// WriteJSON 覆盖写入一个 JSON 文件。
func (recorder *Recorder) WriteJSON(name string, value any) {
	if recorder == nil {
		return
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return
	}
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	_ = os.WriteFile(filepath.Join(recorder.directory, name), append(data, '\n'), 0o600)
}

// AppendJSONL 追加一行带时间戳的 JSONL 记录。
func (recorder *Recorder) AppendJSONL(name, event string, value any) {
	if recorder == nil {
		return
	}
	record := struct {
		Time      string `json:"time"`
		ElapsedMS int64  `json:"elapsed_ms"`
		Event     string `json:"event,omitempty"`
		Data      any    `json:"data"`
	}{
		Time:      time.Now().Format(time.RFC3339Nano),
		ElapsedMS: time.Since(recorder.startedAt).Milliseconds(),
		Event:     event,
		Data:      value,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	file, err := os.OpenFile(filepath.Join(recorder.directory, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(data, '\n'))
}

// WriteError 记录首个失败阶段；后续错误不会覆盖它。
func (recorder *Recorder) WriteError(stage string, err error) {
	if recorder == nil || err == nil {
		return
	}
	path := filepath.Join(recorder.directory, "error.json")
	if _, statErr := os.Stat(path); statErr == nil {
		return
	}
	recorder.WriteJSON("error.json", map[string]any{"stage": stage, "error": err.Error()})
}

// contextKey 是 request context 中 recorder 的私有键类型。
type contextKey struct{}

// WithRecorder 把 recorder 放入请求上下文；nil 时原样返回。
func WithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, recorder)
}

// FromContext 取出请求上下文中的 recorder；不存在时返回 nil。
func FromContext(ctx context.Context) *Recorder {
	recorder, _ := ctx.Value(contextKey{}).(*Recorder)
	return recorder
}
