// 本文件验证图片魔数嗅探与 data URL 解码。
package common

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestSniffImageMIME 覆盖常见图片格式与非法输入。
func TestSniffImageMIME(t *testing.T) {
	cases := map[string]string{
		"png":  "\x89PNG\r\n\x1a\npayload",
		"jpeg": "\xff\xd8\xff\xe0payload",
		"gif":  "GIF89apayload",
		"webp": "RIFF1234WEBPpayload",
	}
	wants := map[string]string{
		"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
	}
	for name, raw := range cases {
		encoded := base64.StdEncoding.EncodeToString([]byte(raw))
		if got := SniffImageMIME(encoded); got != wants[name] {
			t.Fatalf("%s: mime = %q, want %q", name, got, wants[name])
		}
	}
	if got := SniffImageMIME("not-an-image"); got != "" {
		t.Fatalf("invalid input mime = %q", got)
	}
}

// TestDecodeDataImage 验证 data URL、裸 base64 与 http URL 的处理。
func TestDecodeDataImage(t *testing.T) {
	raw := []byte("\x89PNG\r\n\x1a\npayload")
	encoded := base64.StdEncoding.EncodeToString(raw)
	image, err := DecodeDataImage("data:image/png;base64," + encoded)
	if err != nil {
		t.Fatal(err)
	}
	if image.MIMEType != "image/png" || image.Data != encoded {
		t.Fatalf("image = %#v", image)
	}

	image, err = DecodeDataImage("  " + encoded + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if image.MIMEType != "image/png" {
		t.Fatalf("sniffed mime = %q", image.MIMEType)
	}

	if _, err := DecodeDataImage("https://example.com/a.png"); err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("http url error = %v", err)
	}
	if _, err := DecodeDataImage("data:image/png;utf8,not-base64"); err == nil {
		t.Fatal("non-base64 data URL should be rejected")
	}
	if _, err := DecodeDataImage("data:image/png;base64,***"); err == nil {
		t.Fatal("invalid base64 should be rejected")
	}
}
