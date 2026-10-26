// 本文件把中间请求组装为 Devin GetChatMessageRequest。
package devin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Green-hats/devin-2api-lite/internal/llm"
)

// 上游固定参数；改动前先对照 docs/upstream-protocol.md。
const (
	deviceFingerprintBytes = 366
	maxOutputTokens        = 128000
	maxNewlines            = 400
	defaultTemperature     = 1.0
	defaultTopK            = 40
	defaultTopP            = 0.95
)

func buildRequest(request llm.RequestMessages, config Config) (*chatRequest, error) {
	fingerprint, err := randomHex(deviceFingerprintBytes)
	if err != nil {
		return nil, fmt.Errorf("generate Devin device fingerprint: %w", err)
	}
	trajectoryID := randomUUID()
	cascadeID := randomUUID()
	executionID := randomUUID()
	result := &chatRequest{
		metadata: &metadata{
			ideName:          clientName,
			ideVersion:       clientVersion,
			extensionName:    clientName,
			extensionVersion: clientVersion,
			apiKey:           config.Token,
			locale:           "en",
			os:               "mac",
			fingerprint:      fingerprint,
		},
		prompt:       withToolDescriptions(request.SystemPrompt, request.Tools),
		chatModelUID: config.Model,
		requestType:  requestTypeCascade,
		configuration: &completionConfig{
			numCompletions: 1,
			maxTokens:      maxOutputTokens,
			maxNewlines:    maxNewlines,
			temperature:    defaultTemperature,
			topK:           defaultTopK,
			topP:           defaultTopP,
		},
		trajectory: &trajectoryReference{
			trajectoryID:   trajectoryID,
			trajectoryType: trajectoryTypeCascade,
			stepType:       stepTypeUserInput,
		},
		cascadeID:   cascadeID,
		plannerMode: plannerModeDefault,
		executionID: executionID,
	}
	// Devin/Cascade 只可靠接受「当前轮」图片；历史图进 Images 会 invalid_argument。
	// 当前轮 = 最后一条 AssistantMessage 之后的所有 user/tool 消息。
	// Anthropic 客户端常把 image 和 tool_result 放在同一条 user 消息里，
	// 解码后拆成 UserMessage + ToolResultMessage 两条；仅挂最后一条会丢失图片。
	lastAssistantIndex := -1
	for index, message := range request.Messages {
		if _, ok := message.(llm.AssistantMessage); ok {
			lastAssistantIndex = index
		}
	}
	for index, message := range request.Messages {
		converted, err := convertMessage(message, index > lastAssistantIndex)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", index, err)
		}
		result.prompts = append(result.prompts, converted...)
	}
	for _, tool := range request.Tools {
		converted, err := convertToolDefinition(tool)
		if err != nil {
			return nil, err
		}
		result.tools = append(result.tools, converted)
	}
	return result, nil
}

// convertMessage 将中间消息转为 Devin ChatMessagePrompt。
// attachImages 为 true 时才把 ImageContent 写入 Images（仅最新用户轮）；历史图改成文本占位。
func convertMessage(message llm.Message, attachImages bool) ([]*chatPrompt, error) {
	switch message := message.(type) {
	case llm.UserMessage:
		return []*chatPrompt{promptForContent(sourceUser, message.Content, attachImages)}, nil
	case llm.AssistantMessage:
		// 助手历史不回传图片；若有意外 ImageContent 同样占位。
		prompt := promptForContent(sourceSystem, message.Content, false)
		for _, block := range message.Content {
			if call, ok := block.(llm.ToolCall); ok {
				prompt.toolCalls = append(prompt.toolCalls, &toolCall{
					id:            call.ID,
					name:          call.Name,
					argumentsJSON: string(call.Arguments),
				})
			}
		}
		return []*chatPrompt{prompt}, nil
	case llm.ToolResultMessage:
		prompt := promptForContent(sourceTool, message.Content, attachImages)
		prompt.toolCallID = message.ToolCallID
		prompt.toolResultIsError = message.IsError
		return []*chatPrompt{prompt}, nil
	default:
		return nil, fmt.Errorf("unsupported message type %T", message)
	}
}

func promptForContent(source int, content []llm.Content, attachImages bool) *chatPrompt {
	prompt := &chatPrompt{
		messageID: randomUUID(),
		source:    source,
	}
	var text strings.Builder
	for _, block := range content {
		switch block := block.(type) {
		case llm.TextContent:
			text.WriteString(block.Text)
		case llm.ThinkingContent:
			prompt.thinking = block.Thinking
			prompt.signature = block.ThinkingSignature
			prompt.thinkingRedacted = block.Redacted
		case llm.ImageContent:
			if !attachImages {
				// 与 WindsurfAPI 一致：历史图不进 Images，避免上游 invalid_argument。
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString("[Image omitted from history]")
				continue
			}
			// Devin/Windsurf ImageData：纯 base64（无 data: 前缀）+ mime_type。
			data := block.Data
			if strings.HasPrefix(data, "data:") {
				if _, encoded, ok := strings.Cut(data, ","); ok {
					data = encoded
				}
			}
			mimeType := block.MIMEType
			if mimeType == "" {
				mimeType = "image/png"
			}
			prompt.images = append(prompt.images, &imageData{base64Data: data, mimeType: mimeType})
		}
	}
	prompt.prompt = text.String()
	return prompt
}

func randomUUID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func randomHex(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
