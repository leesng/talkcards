package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewLevels(t *testing.T) {
	l, err := New(Options{Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Enabled(nil, slog.LevelWarn) || l.Enabled(nil, slog.LevelInfo) {
		t.Fatal("级别过滤不正确")
	}

	// level parsing is case-insensitive
	if _, err := New(Options{Level: "ERROR", Format: "Text"}); err != nil {
		t.Fatal(err)
	}
}

func TestNewInvalid(t *testing.T) {
	if _, err := New(Options{Level: "verbose"}); err == nil {
		t.Fatal("非法级别应报错")
	}
	if _, err := New(Options{Format: "yaml"}); err == nil {
		t.Fatal("非法格式应报错")
	}
}

func TestTextOutput(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{out: &buf})
	if err != nil {
		t.Fatal(err)
	}
	l.Info("有客户端登录", "user", "张三")
	s := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte(`level=INFO msg=有客户端登录`)) || !bytes.Contains(buf.Bytes(), []byte(`user=张三`)) {
		t.Fatalf("text 输出异常: %s", s)
	}
	if bytes.Contains(buf.Bytes(), []byte(`DEBUG`)) {
		t.Fatalf("默认级别应为 info: %s", s)
	}
}

func TestJSONLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(Options{Level: "error", Format: "json", out: &buf})
	if err != nil {
		t.Fatal(err)
	}
	l.Warn("应被过滤", "desk", 3)
	if buf.Len() != 0 {
		t.Fatalf("warn 不应输出: %s", buf.String())
	}
	l.Error("对局落库失败", "desk", 3, "err", "disk full")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("json 输出异常: %v: %s", err, buf.String())
	}
	if rec["msg"] != "对局落库失败" || rec["desk"] != float64(3) || rec["err"] != "disk full" {
		t.Fatalf("json 字段异常: %v", rec)
	}
}
