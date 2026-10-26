// 本文件定义 Devin RPC 请求/响应共用的上游枚举常量。
package devin

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
