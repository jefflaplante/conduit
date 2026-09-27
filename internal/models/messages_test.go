package models

import (
	"encoding/json"
	"testing"
)

func TestContentBlockMarshal_PerTypeFields(t *testing.T) {
	cc := &CacheControl{Type: "ephemeral", TTL: "1h"}
	cases := []struct {
		name  string
		block ContentBlock
		want  string
	}{
		{"text", TextBlock("hi"), `{"type":"text","text":"hi"}`},
		{"text_cc", ContentBlock{Type: BlockText, Text: "hi", CacheControl: cc}, `{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}`},
		{"image", ContentBlock{Type: BlockImage, Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: "AA=="}}, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}`},
		{"tool_use_nil_input", ContentBlock{Type: BlockToolUse, ID: "t1", Name: "Read"}, `{"type":"tool_use","id":"t1","name":"Read","input":{}}`},
		{"tool_result_empty", ContentBlock{Type: BlockToolResult, ToolUseID: "t1"}, `{"type":"tool_result","tool_use_id":"t1","content":""}`},
		{"tool_result_error", ContentBlock{Type: BlockToolResult, ToolUseID: "t1", Content: "boom", IsError: true, CacheControl: &CacheControl{Type: "ephemeral"}}, `{"type":"tool_result","tool_use_id":"t1","content":"boom","is_error":true,"cache_control":{"type":"ephemeral"}}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.block)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestMessageAndSystemMarshal_StringOrBlocks(t *testing.T) {
	req := MessagesRequest{
		Model: "m", MaxTokens: 1,
		System: &SystemPrompt{Text: "sys"},
		Messages: []Message{
			{Role: "user", Text: ""},
			{Role: "assistant", Blocks: []ContentBlock{TextBlock("a")}},
		},
	}
	got, _ := json.Marshal(req)
	want := `{"model":"m","max_tokens":1,"system":"sys","messages":[{"role":"user","content":""},{"role":"assistant","content":[{"type":"text","text":"a"}]}]}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	req.System = &SystemPrompt{Blocks: []ContentBlock{TextBlock("s")}}
	req.Messages = []Message{}
	req.Stream = true
	got, _ = json.Marshal(req)
	want = `{"model":"m","max_tokens":1,"system":[{"type":"text","text":"s"}],"messages":[],"stream":true}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestMessagesResponse_LenientDecode(t *testing.T) {
	raw := `{"model":"x","stop_reason":"tool_use","usage":{"input_tokens":3.9,"output_tokens":"2","cache_read_input_tokens":7},
	  "content":[{"type":"thinking","thinking":"..."},{"type":"web_search_tool_result","content":[{"a":1}]},"junk",
	  {"type":"tool_use","id":5,"name":"Read","input":{"p":1}},{"type":"text"}]}`
	var r MessagesResponse
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if r.Model != "x" || r.StopReason != "tool_use" || len(r.Content) != 5 {
		t.Fatalf("decoded %+v", r)
	}
	if r.Usage == nil || r.Usage.InputTokens != 3 || r.Usage.OutputTokens != 0 || r.Usage.CacheReadInputTokens != 7 {
		t.Errorf("usage %+v", r.Usage)
	}
	if r.Content[0].Type != "thinking" || r.Content[2].Type != "" {
		t.Errorf("unknown/junk blocks: %+v", r.Content)
	}
	tu := r.Content[3]
	if tu.HasID() || !tu.HasName() || tu.Input == nil {
		t.Errorf("tool_use presence: %+v", tu)
	}
	if r.Content[4].HasText() {
		t.Errorf("absent text reported present")
	}
	var bad MessagesResponse
	if err := json.Unmarshal([]byte(`[1]`), &bad); err == nil {
		t.Error("non-object response must fail")
	}
}

func TestStreamEvent_Decode(t *testing.T) {
	var e StreamEvent
	if err := json.Unmarshal([]byte(`{"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":4}}`), &e); err != nil {
		t.Fatal(err)
	}
	if e.Type != EventMessageDelta || e.Delta == nil || e.Delta.HasStopReason() || e.Usage == nil || e.Usage.OutputTokens != 4 {
		t.Errorf("message_delta: %+v %+v", e, e.Delta)
	}
	e = StreamEvent{}
	_ = json.Unmarshal([]byte(`{"type":"content_block_start","content_block":"x"}`), &e)
	if e.ContentBlock != nil || string(e.RawContentBlock) != `"x"` {
		t.Errorf("non-object content_block: %+v", e)
	}
	e = StreamEvent{}
	_ = json.Unmarshal([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), &e)
	if e.Error == nil || e.Error.Type != "overloaded_error" || e.Error.Message != "Overloaded" {
		t.Errorf("error event: %+v", e.Error)
	}
}
