// 本文件定义 Devin 响应帧及其解码。
package devin

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
