// 本文件定义 Devin 请求侧消息及其编码。
package devin

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
