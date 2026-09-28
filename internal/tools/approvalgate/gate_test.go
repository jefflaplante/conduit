package approvalgate

import (
	"context"
	"errors"
	"testing"
	"time"

	"conduit/internal/approval"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRequester struct {
	action approval.Action
	exec   approval.ExecuteFunc
	err    error
}

func (f *fakeRequester) Request(_ context.Context, a approval.Action, e approval.ExecuteFunc) (*approval.Ticket, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.action, f.exec = a, e
	return &approval.Ticket{ID: "apr_x", Code: "ABC234", Fingerprint: a.Fingerprint, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func testOp() Operation {
	return Operation{Kind: "ssh.exec", Title: "t", Summary: "SSH command on web-1",
		Params: map[string]string{"host": "web-1", "command": "rm -rf /tmp/x"}}
}

func TestRequest_PendingAndDeferredRun(t *testing.T) {
	f := &fakeRequester{}
	op := testOp()
	ran := 0
	res := Request(context.Background(), f, op, func(context.Context) (*types.ToolResult, error) {
		ran++
		return &types.ToolResult{Success: true, Content: "ok"}, nil
	})
	require.True(t, res.Success)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Contains(t, res.Content, "NOT RUN YET")
	assert.NotContains(t, res.Content, "ABC234")
	assert.Equal(t, 0, ran, "run must not execute at request time")
	assert.Equal(t, approval.Fingerprint("ssh.exec", op.Params), f.action.Fingerprint)

	// Caller mutating its params map after the request cannot change the binding.
	op.Params["command"] = "reboot"
	out, err := f.exec(context.Background(), approval.Ticket{Fingerprint: f.action.Fingerprint})
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
	assert.Equal(t, 1, ran)

	// A ticket bound to a different fingerprint is refused.
	_, err = f.exec(context.Background(), approval.Ticket{Fingerprint: "other"})
	assert.Error(t, err)
	assert.Equal(t, 1, ran)
}

func TestRequest_FailedRunSurfacesError(t *testing.T) {
	f := &fakeRequester{}
	Request(context.Background(), f, testOp(), func(context.Context) (*types.ToolResult, error) {
		return &types.ToolResult{Success: false, Error: "exit 1"}, nil
	})
	_, err := f.exec(context.Background(), approval.Ticket{Fingerprint: f.action.Fingerprint})
	assert.EqualError(t, err, "exit 1")
}

func TestRequest_FailsClosed(t *testing.T) {
	run := func(context.Context) (*types.ToolResult, error) {
		t.Fatal("run must not be called")
		return nil, nil
	}
	res := Request(context.Background(), nil, testOp(), run)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_no_approver", res.Data["approval_status"])

	res = Request(context.Background(), &fakeRequester{err: &approval.NonInteractiveError{Source: "cron"}}, testOp(), run)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
	assert.Equal(t, "cron", res.Data["origin"])

	res = Request(context.Background(), &fakeRequester{err: errors.New("boom")}, testOp(), run)
	assert.False(t, res.Success)
	assert.Equal(t, "refused", res.Data["approval_status"])
}

func TestRedactCommand(t *testing.T) {
	cases := map[string]string{
		"ls -la /tmp":                              "ls -la /tmp",
		"DB_PASSWORD=s3cr3t ./migrate":             "DB_PASSWORD=[redacted] ./migrate",
		"mysql --password=s3cr3t -u root":          "mysql --password=[redacted] -u root",
		"vault login --token abc.def":              "vault login --token [redacted]",
		"cli --api-key 'k e y' run":                "cli --api-key [redacted] run",
		"curl https://bob:pw@example.com/x":        "curl https://bob:[redacted]@example.com/x",
		`curl -H "Authorization: Bearer abc123" u`: `curl -H "Authorization: Bearer [redacted]" u`,
		"export MAX_TOKENS=5 && echo $HOME":        "export MAX_TOKENS=5 && echo $HOME",
		"kubectl --namespace prod get pods":        "kubectl --namespace prod get pods",
	}
	for in, want := range cases {
		assert.Equal(t, want, RedactCommand(in), in)
	}
}

func TestClip(t *testing.T) {
	assert.Equal(t, "abc", Clip("abc", 5))
	assert.Equal(t, "ab... [3 more chars]", Clip("abcde", 2))
}
