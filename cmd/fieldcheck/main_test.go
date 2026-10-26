// 本文件验证 proto 字段解析与差异报告。
package main

import "testing"

// TestParseAndVerify 验证字段、oneof 与枚举都能被正确读取。
func TestParseAndVerify(t *testing.T) {
	index, err := parseProto("testdata/mini.proto")
	if err != nil {
		t.Fatal(err)
	}
	fields := []fieldExpectation{
		{"GetChatMessageRequest", "prompt", 2},
		{"ExaCodeiumCommonPb_Metadata", "api_key", 3},
		{"ExaCodeiumCommonPb_ModelOrAlias", "model_uid", 3},
	}
	enums := []enumExpectation{
		{"ExaCodeiumCommonPb_StopReason", "STOP_REASON_ERROR", 13},
	}
	if problems := verify(index, fields, enums); len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
}

// TestVerifyReportsMismatch 验证字段号变化会被报告。
func TestVerifyReportsMismatch(t *testing.T) {
	index, err := parseProto("testdata/mini.proto")
	if err != nil {
		t.Fatal(err)
	}
	problems := verify(index,
		[]fieldExpectation{{"GetChatMessageRequest", "prompt", 9}, {"GetChatMessageRequest", "missing", 1}},
		[]enumExpectation{{"ExaCodeiumCommonPb_StopReason", "STOP_REASON_ERROR", 1}},
	)
	if len(problems) != 3 {
		t.Fatalf("problems = %v", problems)
	}
}
