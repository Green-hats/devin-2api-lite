// fieldcheck 校验本仓库硬编码的 Devin 字段号是否仍与上游 all-protos.proto 一致。
//
// 用法：
//
//	go run ./cmd/fieldcheck /path/to/all-protos.proto
//
// 该文件来自原项目的 proto 提取（outputs/devin-proto/all-protos.proto）。
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// fieldExpectation 是本仓库依赖的一个 protobuf 字段。
type fieldExpectation struct {
	message string
	field   string
	number  int
}

// enumExpectation 是本仓库依赖的一个枚举值。
type enumExpectation struct {
	enum   string
	value  string
	number int
}

// protoIndex 保存从 proto 源码解析出的字段号与枚举值。
type protoIndex struct {
	fields map[string]map[string]int
	enums  map[string]map[string]int
}

var (
	messagePattern = regexp.MustCompile(`^\s*message\s+([A-Za-z0-9_]+)\s*\{`)
	enumPattern    = regexp.MustCompile(`^\s*enum\s+([A-Za-z0-9_]+)\s*\{`)
	fieldPattern   = regexp.MustCompile(`^\s*(?:optional|repeated|required)?\s*[.\w]+\s+([A-Za-z0-9_]+)\s*=\s*(\d+)\s*;`)
	valuePattern   = regexp.MustCompile(`^\s*([A-Za-z0-9_]+)\s*=\s*(-?\d+)\s*;`)
)

// expectations 是本仓库实际读写的字段；与 internal/adapter/devin/types.go 对应。
var expectations = []fieldExpectation{
	{"GetChatMessageRequest", "metadata", 1},
	{"GetChatMessageRequest", "prompt", 2},
	{"GetChatMessageRequest", "chat_message_prompts", 3},
	{"GetChatMessageRequest", "request_type", 7},
	{"GetChatMessageRequest", "configuration", 8},
	{"GetChatMessageRequest", "tools", 10},
	{"GetChatMessageRequest", "trajectory_reference", 15},
	{"GetChatMessageRequest", "cascade_id", 16},
	{"GetChatMessageRequest", "planner_mode", 20},
	{"GetChatMessageRequest", "chat_model_uid", 21},
	{"GetChatMessageRequest", "execution_id", 22},

	{"ExaCodeiumCommonPb_Metadata", "ide_name", 1},
	{"ExaCodeiumCommonPb_Metadata", "extension_version", 2},
	{"ExaCodeiumCommonPb_Metadata", "api_key", 3},
	{"ExaCodeiumCommonPb_Metadata", "locale", 4},
	{"ExaCodeiumCommonPb_Metadata", "os", 5},
	{"ExaCodeiumCommonPb_Metadata", "ide_version", 7},
	{"ExaCodeiumCommonPb_Metadata", "extension_name", 12},
	{"ExaCodeiumCommonPb_Metadata", "f", 31},

	{"ExaChatPb_ChatMessagePrompt", "message_id", 1},
	{"ExaChatPb_ChatMessagePrompt", "source", 2},
	{"ExaChatPb_ChatMessagePrompt", "prompt", 3},
	{"ExaChatPb_ChatMessagePrompt", "tool_calls", 6},
	{"ExaChatPb_ChatMessagePrompt", "tool_call_id", 7},
	{"ExaChatPb_ChatMessagePrompt", "tool_result_is_error", 9},
	{"ExaChatPb_ChatMessagePrompt", "images", 10},
	{"ExaChatPb_ChatMessagePrompt", "thinking", 11},
	{"ExaChatPb_ChatMessagePrompt", "signature", 12},
	{"ExaChatPb_ChatMessagePrompt", "thinking_redacted", 13},

	{"ExaCodeiumCommonPb_ChatToolCall", "id", 1},
	{"ExaCodeiumCommonPb_ChatToolCall", "name", 2},
	{"ExaCodeiumCommonPb_ChatToolCall", "arguments_json", 3},
	{"ExaChatPb_ChatToolDefinition", "name", 1},
	{"ExaChatPb_ChatToolDefinition", "description", 2},
	{"ExaChatPb_ChatToolDefinition", "json_schema_string", 3},
	{"ExaCodeiumCommonPb_ImageData", "base64_data", 1},
	{"ExaCodeiumCommonPb_ImageData", "mime_type", 2},

	{"ExaCodeiumCommonPb_CompletionConfiguration", "num_completions", 1},
	{"ExaCodeiumCommonPb_CompletionConfiguration", "max_tokens", 2},
	{"ExaCodeiumCommonPb_CompletionConfiguration", "max_newlines", 3},
	{"ExaCodeiumCommonPb_CompletionConfiguration", "temperature", 5},
	{"ExaCodeiumCommonPb_CompletionConfiguration", "top_k", 7},
	{"ExaCodeiumCommonPb_CompletionConfiguration", "top_p", 8},

	{"ExaCortexPb_CortexTrajectoryReference", "trajectory_id", 1},
	{"ExaCortexPb_CortexTrajectoryReference", "step_index", 2},
	{"ExaCortexPb_CortexTrajectoryReference", "trajectory_type", 3},
	{"ExaCortexPb_CortexTrajectoryReference", "step_type", 4},

	{"GetChatMessageResponse", "message_id", 1},
	{"GetChatMessageResponse", "timestamp", 2},
	{"GetChatMessageResponse", "delta_text", 3},
	{"GetChatMessageResponse", "stop_reason", 5},
	{"GetChatMessageResponse", "delta_tool_calls", 6},
	{"GetChatMessageResponse", "usage", 7},
	{"GetChatMessageResponse", "delta_thinking", 9},
	{"GetChatMessageResponse", "delta_signature", 10},
	{"GetChatMessageResponse", "thinking_redacted", 11},
	{"GetChatMessageResponse", "request_id", 17},
	{"GetChatMessageResponse", "actual_model_uid", 23},

	{"GoogleProtobuf_Timestamp", "seconds", 1},
	{"GoogleProtobuf_Timestamp", "nanos", 2},

	{"ExaCodeiumCommonPb_ModelUsageStats", "input_tokens", 2},
	{"ExaCodeiumCommonPb_ModelUsageStats", "output_tokens", 3},
	{"ExaCodeiumCommonPb_ModelUsageStats", "cache_write_tokens", 4},
	{"ExaCodeiumCommonPb_ModelUsageStats", "cache_read_tokens", 5},
	{"ExaCodeiumCommonPb_ModelUsageStats", "api_provider", 6},
	{"ExaCodeiumCommonPb_ModelUsageStats", "model_uid", 9},

	{"GetCascadeModelConfigsRequest", "metadata", 1},
	{"GetCascadeModelConfigsResponse", "client_model_configs", 1},
	{"ExaCodeiumCommonPb_ClientModelConfig", "label", 1},
	{"ExaCodeiumCommonPb_ClientModelConfig", "model_or_alias", 2},
	{"ExaCodeiumCommonPb_ClientModelConfig", "disabled", 4},
	{"ExaCodeiumCommonPb_ClientModelConfig", "supports_images", 5},
	{"ExaCodeiumCommonPb_ClientModelConfig", "provider", 10},
	{"ExaCodeiumCommonPb_ClientModelConfig", "max_tokens", 18},
	{"ExaCodeiumCommonPb_ClientModelConfig", "model_uid", 22},
	{"ExaCodeiumCommonPb_ClientModelConfig", "description", 27},
	{"ExaCodeiumCommonPb_ClientModelConfig", "model_family_metadata", 30},
	{"ExaCodeiumCommonPb_ClientModelConfig", "is_default_model_in_family", 31},
	{"ExaCodeiumCommonPb_ModelFamilyMetadata", "model_family_label", 1},
	{"ExaCodeiumCommonPb_ModelFamilyMetadata", "entries", 2},
	{"ExaCodeiumCommonPb_ModelFamilyMetadata", "is_default_model_in_family", 3},
	{"ExaCodeiumCommonPb_ModelFamilyMetadataEntry", "key", 1},
	{"ExaCodeiumCommonPb_ModelFamilyMetadataEntry", "value", 2},
	{"ExaCodeiumCommonPb_ModelFamilyMetadataValue", "order", 1},
	{"ExaCodeiumCommonPb_ModelFamilyMetadataValue", "name", 2},
	{"ExaCodeiumCommonPb_ModelOrAlias", "model_uid", 3},
}

// enumExpectations 是本仓库依赖的枚举值。
var enumExpectations = []enumExpectation{
	{"ExaCodeiumCommonPb_StopReason", "STOP_REASON_INCOMPLETE", 1},
	{"ExaCodeiumCommonPb_StopReason", "STOP_REASON_MAX_TOKENS", 3},
	{"ExaCodeiumCommonPb_StopReason", "STOP_REASON_PARTIAL", 9},
	{"ExaCodeiumCommonPb_StopReason", "STOP_REASON_FUNCTION_CALL", 10},
	{"ExaCodeiumCommonPb_StopReason", "STOP_REASON_ERROR", 13},
	{"ExaCodeiumCommonPb_ChatMessageSource", "CHAT_MESSAGE_SOURCE_USER", 1},
	{"ExaCodeiumCommonPb_ChatMessageSource", "CHAT_MESSAGE_SOURCE_SYSTEM", 2},
	{"ExaCodeiumCommonPb_ChatMessageSource", "CHAT_MESSAGE_SOURCE_TOOL", 4},
	{"ChatMessageRequestType", "CHAT_MESSAGE_REQUEST_TYPE_CASCADE", 5},
	{"ExaCodeiumCommonPb_ConversationalPlannerMode", "CONVERSATIONAL_PLANNER_MODE_DEFAULT", 1},
	{"ExaCortexPb_CortexTrajectoryType", "CORTEX_TRAJECTORY_TYPE_CASCADE", 4},
	{"ExaCortexPb_CortexStepType", "CORTEX_STEP_TYPE_USER_INPUT", 14},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_WINDSURF", 1},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_OPENAI", 2},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_ANTHROPIC", 3},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_GOOGLE", 4},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_XAI", 5},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_DEEPSEEK", 6},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_MOONSHOT", 7},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_QWEN", 8},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_ZAI", 9},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_MINIMAX", 10},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_NVIDIA", 11},
	{"ExaCodeiumCommonPb_ModelProvider", "MODEL_PROVIDER_THINKING_MACHINES", 12},
}

// parseProto 提取顶层 message/enum 的字段号；忽略注释与嵌套声明。
func parseProto(path string) (*protoIndex, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	index := &protoIndex{fields: map[string]map[string]int{}, enums: map[string]map[string]int{}}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	message := ""
	enum := ""
	depth := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "//") || line == "" {
			continue
		}
		if match := messagePattern.FindStringSubmatch(line); match != nil {
			message = match[1]
			enum = ""
			depth = 1
			if _, ok := index.fields[message]; !ok {
				index.fields[message] = map[string]int{}
			}
			continue
		}
		if match := enumPattern.FindStringSubmatch(line); match != nil {
			enum = match[1]
			message = ""
			depth = 1
			if _, ok := index.enums[enum]; !ok {
				index.enums[enum] = map[string]int{}
			}
			continue
		}
		if depth == 0 {
			continue
		}
		switch {
		case message != "":
			if match := fieldPattern.FindStringSubmatch(line); match != nil {
				number, _ := strconv.Atoi(match[2])
				index.fields[message][match[1]] = number
			}
		case enum != "":
			if match := valuePattern.FindStringSubmatch(line); match != nil {
				number, _ := strconv.Atoi(match[2])
				index.enums[enum][match[1]] = number
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			message, enum, depth = "", "", 0
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return index, nil
}

// verify 返回 expectations 与 proto 索引之间的差异。
func verify(index *protoIndex, fields []fieldExpectation, enums []enumExpectation) []string {
	var problems []string
	for _, expectation := range fields {
		actual, ok := index.fields[expectation.message][expectation.field]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("missing field %s.%s", expectation.message, expectation.field))
		case actual != expectation.number:
			problems = append(problems, fmt.Sprintf("field %s.%s: got %d, want %d", expectation.message, expectation.field, actual, expectation.number))
		}
	}
	for _, expectation := range enums {
		values := index.enums[expectation.enum]
		found := false
		for name, number := range values {
			if !strings.HasSuffix(name, expectation.value) {
				continue
			}
			found = true
			if number != expectation.number {
				problems = append(problems, fmt.Sprintf("enum %s.%s: got %d, want %d", expectation.enum, name, number, expectation.number))
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("missing enum %s.%s", expectation.enum, expectation.value))
		}
	}
	return problems
}

// run 解析 proto 并返回差异列表。
func run(path string) ([]string, error) {
	index, err := parseProto(path)
	if err != nil {
		return nil, err
	}
	return verify(index, expectations, enumExpectations), nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <all-protos.proto>\n", os.Args[0])
		os.Exit(2)
	}
	problems, err := run(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintln(os.Stderr, problem)
		}
		fmt.Fprintf(os.Stderr, "%d field check(s) failed\n", len(problems))
		os.Exit(1)
	}
	fmt.Println("field check passed")
}
