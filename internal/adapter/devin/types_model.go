// 本文件定义 Devin 模型目录元数据及其解码。
package devin

import "strings"

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
