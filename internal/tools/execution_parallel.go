package tools

import (
	"context"
	"log"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"conduit/internal/ai"
	"conduit/internal/sandbox"
)

// executeParallel executes multiple tools in parallel with controlled
// concurrency. conduit-31jg.58: calls touching the same file, at least one of
// them writing it, run sequentially in model order; all others stay parallel.
func (e *ExecutionEngine) executeParallel(ctx context.Context, calls []ai.ToolCall) []*ExecutionResult {
	results := make([]*ExecutionResult, len(calls))

	// Use worker pool for controlled concurrency. A slot is held per call,
	// not per group, so a serialized group never pins more than one slot.
	limit := e.maxParallel
	if limit < 1 {
		limit = 1
	}
	semaphore := make(chan struct{}, limit)
	var wg sync.WaitGroup

	runOne := func(idx int) {
		toolCall := calls[idx]
		semaphore <- struct{}{}
		defer func() { <-semaphore }()

		// conduit-31jg.47: never let one call's panic kill the process.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[ExecutionEngine] PANIC in parallel worker for tool %q: %v\n%s", toolCall.Name, rec, debug.Stack())
				results[idx] = pipelinePanicResult(toolCall, time.Now(), rec)
			}
		}()

		results[idx] = e.executeSingle(ctx, toolCall)
	}

	for _, group := range e.pathGroups(calls) {
		wg.Add(1)
		go func(group []int) {
			defer wg.Done()
			for _, idx := range group {
				runOne(idx)
			}
		}(group)
	}

	wg.Wait()
	return results
}

// pathGroups partitions call indices into groups that may run concurrently
// with one another; each group runs in model order. conduit-31jg.58.
//
// Read is included: a Read of a file that is also written in the same turn is
// ordered relative to that write as the model issued them, so it sees the
// state the model expects rather than a stale or half-written file. A path
// that is only read is not serialized: reads cannot clobber each other.
func (e *ExecutionEngine) pathGroups(calls []ai.ToolCall) [][]int {
	keys := make([]string, len(calls))
	written := map[string]bool{}
	for i, c := range calls {
		key, write := e.fileAccess(c)
		keys[i] = key
		if key != "" && write {
			written[key] = true
		}
	}
	var groups [][]int
	byKey := map[string]int{} // canonical path -> index into groups
	for i, key := range keys {
		if key == "" || !written[key] {
			groups = append(groups, []int{i})
			continue
		}
		if g, ok := byKey[key]; ok {
			groups[g] = append(groups[g], i)
			continue
		}
		byKey[key] = len(groups)
		groups = append(groups, []int{i})
	}
	return groups
}

// fileAccess returns the canonical local path a call reads or writes and
// whether it writes it; key is "" for calls that are not file tools.
// Bash is not classified: its file effects can't be known from its args.
// conduit-31jg.58.
func (e *ExecutionEngine) fileAccess(call ai.ToolCall) (key string, write bool) {
	str := func(k string) string { s, _ := call.Args[k].(string); return s }
	var p string
	workspaceRelative := true
	switch call.Name {
	case "Write":
		p, write = str("path"), true
	case "Edit":
		p, write = str("path"), true
		if p == "" {
			p = str("file_path") // EditTool's alternative param name
		}
	case "Read":
		p = str("path")
	case "Ssh": // scp moves a local file; local_path is used as given
		workspaceRelative = false
		switch str("action") {
		case "scp_download":
			p, write = str("local_path"), true
		case "scp_upload":
			p = str("local_path")
		}
	}
	if p == "" {
		return "", false
	}
	if workspaceRelative && !filepath.IsAbs(p) {
		if r, ok := e.registry.(interface{ workspacePathBase() string }); ok {
			if base := r.workspacePathBase(); base != "" {
				p = filepath.Join(base, p)
			}
		}
	}
	if real, err := sandbox.Canonicalize(p); err == nil {
		return real, write
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs, write
	}
	return filepath.Clean(p), write
}

// workspacePathBase is the directory Read/Write/Edit resolve relative paths
// against (see their resolvePath). conduit-31jg.58.
func (r *Registry) workspacePathBase() string {
	if r.services != nil && r.services.ConfigMgr != nil {
		if d := r.services.ConfigMgr.Workspace.ContextDir; d != "" {
			return d
		}
		if d := r.services.ConfigMgr.Tools.Sandbox.WorkspaceDir; d != "" {
			return d
		}
	}
	return r.sandboxCfg.WorkspaceDir
}
