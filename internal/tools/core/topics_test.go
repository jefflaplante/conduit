package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"conduit/internal/tools/types"
)

// fakeTopicGateway records topic_* calls (conduit-43vk).
type fakeTopicGateway struct {
	types.GatewayService
	calls []string
	err   error
}

func (f *fakeTopicGateway) rec(call string) (string, error) {
	f.calls = append(f.calls, call)
	if f.err != nil {
		return "", f.err
	}
	return "ok: " + call, nil
}

func (f *fakeTopicGateway) TopicList(context.Context) (string, error) { return f.rec("list") }
func (f *fakeTopicGateway) TopicCreate(_ context.Context, slug, name, persona string) (string, error) {
	return f.rec("create " + slug + "|" + name + "|" + persona)
}
func (f *fakeTopicGateway) TopicSwitch(_ context.Context, slug string) (string, error) {
	return f.rec("switch " + slug)
}
func (f *fakeTopicGateway) TopicRename(_ context.Context, slug, newName, newSlug string) (string, error) {
	return f.rec("rename " + slug + "|" + newName + "|" + newSlug)
}
func (f *fakeTopicGateway) TopicPark(_ context.Context, slug string) (string, error) {
	return f.rec("park " + slug)
}
func (f *fakeTopicGateway) TopicClose(_ context.Context, slug string) (string, error) {
	return f.rec("close " + slug)
}
func (f *fakeTopicGateway) TopicStitch(_ context.Context, from, to string) (string, error) {
	return f.rec("stitch " + from + "|" + to)
}
func (f *fakeTopicGateway) TopicPersona(_ context.Context, slug, persona string) (string, error) {
	return f.rec("persona " + slug + "|" + persona)
}

func (f *fakeTopicGateway) TopicProfile(_ context.Context, slug, profile string) (string, error) {
	return f.rec("profile " + slug + "|" + profile)
}

func findTopicTool(t *testing.T, tools []types.Tool, name string) types.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("tool %s not registered", name)
	return nil
}

func TestTopicTools(t *testing.T) {
	tests := []struct {
		tool     string
		required []string
		args     map[string]interface{}
		wantCall string
	}{
		{tool: "topic_list", args: map[string]interface{}{}, wantCall: "list"},
		{tool: "topic_create", required: []string{"slug"},
			args:     map[string]interface{}{"slug": "alpha", "name": "Alpha", "persona": "Be terse."},
			wantCall: "create alpha|Alpha|Be terse."},
		{tool: "topic_switch", required: []string{"slug"},
			args: map[string]interface{}{"slug": "alpha"}, wantCall: "switch alpha"},
		{tool: "topic_rename", required: []string{"slug"},
			args:     map[string]interface{}{"slug": "alpha", "new_name": "Alpha Two", "new_slug": "alpha-two"},
			wantCall: "rename alpha|Alpha Two|alpha-two"},
		{tool: "topic_park", required: []string{"slug"},
			args: map[string]interface{}{"slug": "alpha"}, wantCall: "park alpha"},
		{tool: "topic_close", required: []string{"slug"},
			args: map[string]interface{}{"slug": "alpha"}, wantCall: "close alpha"},
		{tool: "topic_stitch", required: []string{"from", "to"},
			args: map[string]interface{}{"from": "alpha", "to": "beta"}, wantCall: "stitch alpha|beta"},
		{tool: "topic_persona", required: []string{"slug"},
			args: map[string]interface{}{"slug": "alpha", "persona": ""}, wantCall: "persona alpha|"},
		{tool: "topic_profile", required: []string{"slug"},
			args:     map[string]interface{}{"slug": "alpha", "profile": `{"skills":["gog"]}`},
			wantCall: `profile alpha|{"skills":["gog"]}`},
	}

	if got := len(NewTopicTools(nil)); got != len(tests) {
		t.Fatalf("NewTopicTools returned %d tools, want %d", got, len(tests))
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			ctx := context.Background()
			gw := &fakeTopicGateway{}
			tool := findTopicTool(t, NewTopicTools(&types.ToolServices{Gateway: gw}), tt.tool)

			// Schema.
			if tool.Description() == "" {
				t.Fatal("empty description")
			}
			params := tool.Parameters()
			if params["type"] != "object" {
				t.Fatalf("schema type = %v", params["type"])
			}
			props, _ := params["properties"].(map[string]interface{})
			req, _ := params["required"].([]string)
			if strings.Join(req, ",") != strings.Join(tt.required, ",") {
				t.Fatalf("required = %v, want %v", req, tt.required)
			}
			for k := range tt.args {
				if _, ok := props[k]; !ok {
					t.Fatalf("schema lacks property %q", k)
				}
			}
			if _, ok := tool.(types.EnhancedSchemaProvider); !ok {
				t.Fatalf("%s does not provide schema hints", tt.tool)
			}

			// Happy path.
			res, err := tool.Execute(ctx, tt.args)
			if err != nil || !res.Success || res.Content != "ok: "+tt.wantCall {
				t.Fatalf("execute = %+v, %v", res, err)
			}

			// Missing required args.
			if len(tt.required) > 0 {
				res, _ = tool.Execute(ctx, map[string]interface{}{})
				if res.Success || !strings.Contains(res.Error, tt.required[0]) {
					t.Fatalf("missing args = %+v", res)
				}
			}

			// Gateway error.
			gw.err = errors.New("topic not found: nope")
			res, _ = tool.Execute(ctx, tt.args)
			if res.Success || !strings.Contains(res.Error, "topic not found") {
				t.Fatalf("gateway error = %+v", res)
			}

			// No topic-capable gateway.
			bare := findTopicTool(t, NewTopicTools(&types.ToolServices{Gateway: plainGateway{}}), tt.tool)
			res, _ = bare.Execute(ctx, tt.args)
			if res.Success || !strings.Contains(res.Error, "not available") {
				t.Fatalf("no topic gateway = %+v", res)
			}

			// SelfTest.
			st, ok := tool.(types.SelfTester)
			if !ok {
				t.Fatal("no SelfTest")
			}
			if r := st.SelfTest(ctx, &types.SelfTestOptions{}); r.Status != types.SelfTestStatusOK {
				t.Fatalf("selftest = %+v", r)
			}
			if r := bare.(types.SelfTester).SelfTest(ctx, &types.SelfTestOptions{}); r.Status != types.SelfTestStatusFailed {
				t.Fatalf("bare selftest = %+v", r)
			}
		})
	}
}

// conduit-331g: models often send the profile as a JSON object rather than a
// string; the tool re-encodes it so the gateway always validates JSON text.
func TestTopicProfileTool_ObjectArgument(t *testing.T) {
	gw := &fakeTopicGateway{}
	tool := findTopicTool(t, NewTopicTools(&types.ToolServices{Gateway: gw}), "topic_profile")
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"slug":    "alpha",
		"profile": map[string]interface{}{"skills": []interface{}{"gog"}, "skip_context": []interface{}{"TOOLS.md"}},
	})
	if err != nil || !res.Success {
		t.Fatalf("execute = %+v, %v", res, err)
	}
	if want := `profile alpha|{"skills":["gog"],"skip_context":["TOOLS.md"]}`; gw.calls[0] != want {
		t.Fatalf("call = %q, want %q", gw.calls[0], want)
	}
	if res, _ := tool.Execute(context.Background(), map[string]interface{}{"slug": "alpha"}); !res.Success || gw.calls[1] != "profile alpha|" {
		t.Fatalf("absent profile should clear: %+v %q", res, gw.calls)
	}
}
