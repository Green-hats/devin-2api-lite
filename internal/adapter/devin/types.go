// 本文件定义 Devin RPC 请求/响应中本适配器使用的字段子集及其编解码。
package devin

import "strings"

// 上游枚举数值（与 all-protos.proto 中的定义一致）。
const (
	sourceUser   = 1
	sourceSystem = 2
	sourceTool   = 4

	requestTypeCascade = 5

	plannerModeDefault = 1

	trajectoryTypeCascade = 4
	stepTypeUserInput     = 14

	stopReasonIncomplete   = 1
	stopReasonMaxTokens    = 3
	stopReasonPartial      = 9
	stopReasonFunctionCall = 10
	stopReasonError        = 13
)

// metadata 对应 ExaCodeiumCommonPb_Metadata 的客户端身份字段。
type metadata struct {
	ideName          string
	ideVersion       string
	extensionName    string
	extensionVersion string
	apiKey           string
	locale           string
	os               string
	fingerprint      string
}

// encode 按 ExaCodeiumCommonPb_Metadata 的字段编号写入。
func (m *metadata) encode(b *buffer) {
	b.str(1, m.ideName)
	b.str(2, m.extensionVersion)
	b.str(3, m.apiKey)
	b.str(4, m.locale)
	b.str(5, m.os)
	b.str(7, m.ideVersion)
	b.str(12, m.extensionName)
	b.str(31, m.fingerprint)
}

// completionConfig 对应 ExaCodeiumCommonPb_CompletionConfiguration。
type completionConfig struct {
	numCompletions uint64
	maxTokens      uint64
	maxNewlines    uint64
	temperature    float64
	topK           uint64
	topP           float64
}

func (c *completionConfig) encode(b *buffer) {
	b.uint(1, c.numCompletions)
	b.uint(2, c.maxTokens)
	b.uint(3, c.maxNewlines)
	b.double(5, c.temperature)
	b.uint(7, c.topK)
	b.double(8, c.topP)
}

// trajectoryReference 对应 ExaCortexPb_CortexTrajectoryReference。
type trajectoryReference struct {
	trajectoryID   string
	stepIndex      int64
	trajectoryType int
	stepType       int
}

func (t *trajectoryReference) encode(b *buffer) {
	b.str(1, t.trajectoryID)
	b.uint(2, uint64(t.stepIndex))
	b.uint(3, uint64(t.trajectoryType))
	b.uint(4, uint64(t.stepType))
}

// toolCall 对应 ExaCodeiumCommonPb_ChatToolCall。
type toolCall struct {
	id            string
	name          string
	argumentsJSON string
}

func (c *toolCall) encode(b *buffer) {
	b.str(1, c.id)
	b.str(2, c.name)
	b.str(3, c.argumentsJSON)
}

func decodeToolCall(data []byte) (*toolCall, error) {
	r := newReader(data)
	call := &toolCall{}
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			call.id, _ = r.str()
		case 2:
			call.name, _ = r.str()
		case 3:
			call.argumentsJSON, _ = r.str()
		default:
			r.skip()
		}
	}
	if err := r.errOrNil(); err != nil {
		return nil, err
	}
	return call, nil
}

// imageData 对应 ExaCodeiumCommonPb_ImageData。
type imageData struct {
	base64Data string
	mimeType   string
}

func (i *imageData) encode(b *buffer) {
	b.str(1, i.base64Data)
	b.str(2, i.mimeType)
}

// chatPrompt 对应 ExaChatPb_ChatMessagePrompt。
type chatPrompt struct {
	messageID         string
	source            int
	prompt            string
	toolCalls         []*toolCall
	toolCallID        string
	toolResultIsError bool
	images            []*imageData
	thinking          string
	signature         string
	thinkingRedacted  bool
}

func (p *chatPrompt) encode(b *buffer) {
	b.str(1, p.messageID)
	b.uint(2, uint64(p.source))
	b.str(3, p.prompt)
	for _, call := range p.toolCalls {
		b.message(6, call.encode)
	}
	b.str(7, p.toolCallID)
	b.boolean(9, p.toolResultIsError)
	for _, image := range p.images {
		b.message(10, image.encode)
	}
	b.str(11, p.thinking)
	b.str(12, p.signature)
	b.boolean(13, p.thinkingRedacted)
}

// toolDefinition 对应 ExaChatPb_ChatToolDefinition。
type toolDefinition struct {
	name       string
	schemaJSON string
}

func (t *toolDefinition) encode(b *buffer) {
	b.str(1, t.name)
	// 上游会把 description 当作工具分类提示，这里只回填工具名；
	// 真正的说明通过 system prompt 注入（见 tool_definition.go）。
	b.str(2, t.name)
	b.str(3, t.schemaJSON)
}

// chatRequest 对应 GetChatMessageRequest 中本适配器设置的字段。
type chatRequest struct {
	metadata      *metadata
	prompt        string
	prompts       []*chatPrompt
	chatModelUID  string
	requestType   int
	configuration *completionConfig
	tools         []*toolDefinition
	trajectory    *trajectoryReference
	cascadeID     string
	plannerMode   int
	executionID   string
}

// encode 序列化为 GetChatMessageRequest。
func (r *chatRequest) encode() []byte {
	var b buffer
	b.message(1, r.metadata.encode)
	b.str(2, r.prompt)
	for _, prompt := range r.prompts {
		b.message(3, prompt.encode)
	}
	b.uint(7, uint64(r.requestType))
	b.message(8, r.configuration.encode)
	for _, tool := range r.tools {
		b.message(10, tool.encode)
	}
	b.message(15, r.trajectory.encode)
	b.str(16, r.cascadeID)
	b.uint(20, uint64(r.plannerMode))
	b.str(21, r.chatModelUID)
	b.str(22, r.executionID)
	return b.data
}

// modelConfigsRequest 对应 GetCascadeModelConfigsRequest。
type modelConfigsRequest struct {
	metadata *metadata
}

func (r *modelConfigsRequest) encode() []byte {
	var b buffer
	b.message(1, r.metadata.encode)
	return b.data
}

// timestamp 对应 GoogleProtobuf_Timestamp。
type timestamp struct {
	seconds int64
	nanos   int32
}

func decodeTimestamp(data []byte) (*timestamp, error) {
	r := newReader(data)
	value := &timestamp{}
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			value.seconds, _ = r.i64()
		case 2:
			nanos, _ := r.u64()
			value.nanos = int32(nanos)
		default:
			r.skip()
		}
	}
	if err := r.errOrNil(); err != nil {
		return nil, err
	}
	return value, nil
}

// usageStats 对应 ExaCodeiumCommonPb_ModelUsageStats。
// 数值字段保留指针形式，用于区分“未提供”和“值为 0”。
type usageStats struct {
	modelUID     string
	inputTokens  *uint64
	outputTokens *uint64
	cacheWrite   *uint64
	cacheRead    *uint64
	apiProvider  int
}

func decodeUsageStats(data []byte) (*usageStats, error) {
	r := newReader(data)
	usage := &usageStats{}
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 2:
			if value, ok := r.u64(); ok {
				usage.inputTokens = &value
			}
		case 3:
			if value, ok := r.u64(); ok {
				usage.outputTokens = &value
			}
		case 4:
			if value, ok := r.u64(); ok {
				usage.cacheWrite = &value
			}
		case 5:
			if value, ok := r.u64(); ok {
				usage.cacheRead = &value
			}
		case 6:
			value, _ := r.u64()
			usage.apiProvider = int(value)
		case 9:
			usage.modelUID, _ = r.str()
		default:
			r.skip()
		}
	}
	if err := r.errOrNil(); err != nil {
		return nil, err
	}
	return usage, nil
}

// chatResponse 对应 GetChatMessageResponse 中本适配器读取的字段。
type chatResponse struct {
	messageID        string
	timestamp        *timestamp
	deltaText        string
	stopReason       int
	deltaToolCalls   []*toolCall
	usage            *usageStats
	deltaThinking    string
	deltaSignature   string
	thinkingRedacted bool
	actualModelUID   string
	requestID        string
}

// decodeChatResponse 解析一帧 GetChatMessageResponse。
func decodeChatResponse(data []byte) (*chatResponse, error) {
	r := newReader(data)
	response := &chatResponse{}
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			response.messageID, _ = r.str()
		case 2:
			if raw, ok := r.bytes(); ok {
				value, err := decodeTimestamp(raw)
				if err != nil {
					return nil, err
				}
				response.timestamp = value
			}
		case 3:
			response.deltaText, _ = r.str()
		case 5:
			reason, _ := r.u64()
			response.stopReason = int(reason)
		case 6:
			if raw, ok := r.bytes(); ok {
				call, err := decodeToolCall(raw)
				if err != nil {
					return nil, err
				}
				response.deltaToolCalls = append(response.deltaToolCalls, call)
			}
		case 7:
			if raw, ok := r.bytes(); ok {
				usage, err := decodeUsageStats(raw)
				if err != nil {
					return nil, err
				}
				response.usage = usage
			}
		case 9:
			response.deltaThinking, _ = r.str()
		case 10:
			response.deltaSignature, _ = r.str()
		case 11:
			value, _ := r.u64()
			response.thinkingRedacted = value != 0
		case 17:
			response.requestID, _ = r.str()
		case 23:
			response.actualModelUID, _ = r.str()
		default:
			r.skip()
		}
	}
	if err := r.errOrNil(); err != nil {
		return nil, err
	}
	return response, nil
}

// clientModelConfig 对应 ExaCodeiumCommonPb_ClientModelConfig 的展示字段。
type clientModelConfig struct {
	modelUID        string
	label           string
	description     string
	maxTokens       int64
	disabled        bool
	supportsImages  bool
	provider        int
	defaultInFamily bool
	family          *modelFamilyMetadata
}

// modelFamilyMetadata 对应 ExaCodeiumCommonPb_ModelFamilyMetadata。
type modelFamilyMetadata struct {
	label           string
	defaultInFamily bool
	entries         []modelFamilyEntry
}

// modelFamilyEntry 是 family metadata 里的一个键值项。
type modelFamilyEntry struct {
	key   string
	name  string
	order int64
}

// effort 从 family metadata 读取推理档位。
func (config *clientModelConfig) effort() string {
	if config.family == nil {
		return ""
	}
	for _, entry := range config.family.entries {
		switch strings.ToLower(entry.key) {
		case "reasoning effort", "effort", "thinking":
			return strings.ToLower(entry.name)
		}
	}
	return ""
}

// fastMode 从 family metadata 读取 fast 变体标记。
func (config *clientModelConfig) fastMode() bool {
	if config.family == nil {
		return false
	}
	for _, entry := range config.family.entries {
		if strings.EqualFold(entry.key, "fast mode") {
			return entry.order > 0
		}
	}
	return false
}

// decodeClientModelConfig 解析单个模型配置项。
func decodeClientModelConfig(data []byte) (*clientModelConfig, error) {
	r := newReader(data)
	config := &clientModelConfig{}
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			config.label, _ = r.str()
		case 2:
			// model_or_alias 是 oneof；只关心其中的 string model_uid。
			if raw, ok := r.bytes(); ok {
				if uid := decodeModelOrAliasUID(raw); uid != "" {
					config.modelUID = uid
				}
			}
		case 4:
			value, _ := r.u64()
			config.disabled = value != 0
		case 5:
			value, _ := r.u64()
			config.supportsImages = value != 0
		case 10:
			value, _ := r.u64()
			config.provider = int(value)
		case 18:
			value, _ := r.u64()
			config.maxTokens = int64(value)
		case 22:
			config.modelUID, _ = r.str()
		case 27:
			config.description, _ = r.str()
		case 30:
			if raw, ok := r.bytes(); ok {
				family, err := decodeFamilyMetadata(raw)
				if err != nil {
					return nil, err
				}
				config.family = family
			}
		case 31:
			value, _ := r.u64()
			config.defaultInFamily = value != 0
		default:
			r.skip()
		}
	}
	if err := r.errOrNil(); err != nil {
		return nil, err
	}
	return config, nil
}

// decodeFamilyMetadata 解析 ModelFamilyMetadata。
func decodeFamilyMetadata(data []byte) (*modelFamilyMetadata, error) {
	r := newReader(data)
	family := &modelFamilyMetadata{}
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			family.label, _ = r.str()
		case 2:
			raw, ok := r.bytes()
			if !ok {
				continue
			}
			entry, err := decodeFamilyEntry(raw)
			if err != nil {
				return nil, err
			}
			family.entries = append(family.entries, entry)
		case 3:
			value, _ := r.u64()
			family.defaultInFamily = value != 0
		default:
			r.skip()
		}
	}
	if err := r.errOrNil(); err != nil {
		return nil, err
	}
	return family, nil
}

// decodeFamilyEntry 解析 family metadata 的单个 entry。
func decodeFamilyEntry(data []byte) (modelFamilyEntry, error) {
	r := newReader(data)
	entry := modelFamilyEntry{}
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		switch field {
		case 1:
			entry.key, _ = r.str()
		case 2:
			raw, ok := r.bytes()
			if !ok {
				continue
			}
			valueReader := newReader(raw)
			for {
				valueField, ok := valueReader.next()
				if !ok {
					break
				}
				switch valueField {
				case 1:
					entry.order, _ = valueReader.i64()
				case 2:
					entry.name, _ = valueReader.str()
				default:
					valueReader.skip()
				}
			}
			if err := valueReader.errOrNil(); err != nil {
				return entry, err
			}
		default:
			r.skip()
		}
	}
	if err := r.errOrNil(); err != nil {
		return entry, err
	}
	return entry, nil
}

// decodeModelOrAliasUID 仅读取 oneof 中的 string model_uid 分支。
func decodeModelOrAliasUID(data []byte) string {
	r := newReader(data)
	uid := ""
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		if field == 3 {
			uid, _ = r.str()
			continue
		}
		r.skip()
	}
	return uid
}

// modelConfigsResponse 对应 GetCascadeModelConfigsResponse。
type modelConfigsResponse struct {
	configs []*clientModelConfig
}

// decodeModelConfigs 解析模型目录响应。
func decodeModelConfigs(data []byte) (*modelConfigsResponse, error) {
	r := newReader(data)
	response := &modelConfigsResponse{}
	for {
		field, ok := r.next()
		if !ok {
			break
		}
		if field != 1 {
			r.skip()
			continue
		}
		raw, ok := r.bytes()
		if !ok {
			continue
		}
		config, err := decodeClientModelConfig(raw)
		if err != nil {
			return nil, err
		}
		response.configs = append(response.configs, config)
	}
	if err := r.errOrNil(); err != nil {
		return nil, err
	}
	return response, nil
}
