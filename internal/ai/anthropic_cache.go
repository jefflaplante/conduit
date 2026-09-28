package ai

import (
	"encoding/json"

	"conduit/internal/models"
)

// maxCacheBreakpoints is the Anthropic limit on cache_control markers per
// request (tools + system + messages combined). conduit-31jg.14.
const maxCacheBreakpoints = 4

// imageTokenEstimate is a flat per-image estimate; base64 length would
// wildly overstate an image's token cost.
const imageTokenEstimate = 1600

// estimateTokens is the rough 4-chars-per-token estimate used for cache
// thresholds.
func estimateTokens(s string) int { return len(s) / 4 }

// estimateJSONTokens estimates the tokens of an arbitrary request fragment.
func estimateJSONTokens(v interface{}) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b) / 4
}

// estimateMessageTokens estimates one converted message. conduit-31jg.14:
// the old estimate counted only string content, so tool turns (block
// arrays: tool_use / tool_result / text) counted as zero.
func estimateMessageTokens(msg models.Message) int {
	if !msg.HasBlocks() {
		return estimateTokens(msg.Text)
	}
	n := 0
	for _, block := range msg.Blocks {
		if block.Type == models.BlockImage {
			n += imageTokenEstimate
			continue
		}
		n += estimateJSONTokens(block)
	}
	return n
}

// markMessageBreakpoint puts cc on one content block of msg and reports
// whether it did. String content is converted to a single text block. In a
// block array the last tool_result is preferred: text after it is ephemeral
// loop guidance (conduit-31jg.13) that the next request strips, so caching
// through it would write an entry that is never read.
func markMessageBreakpoint(msg *models.Message, cc *models.CacheControl) bool {
	if !msg.HasBlocks() {
		if msg.Text == "" {
			return false
		}
		block := models.TextBlock(msg.Text)
		block.CacheControl = cc
		msg.Blocks, msg.Text = []models.ContentBlock{block}, ""
		return true
	}
	c := msg.Blocks
	target := -1
	for i := len(c) - 1; i >= 0; i-- {
		if c[i].Type == models.BlockToolResult {
			target = i
			break
		}
	}
	if target < 0 {
		target = len(c) - 1
	}
	if target < 0 {
		return false
	}
	if c[target].Type == models.BlockText && c[target].Text == "" {
		return false
	}
	c[target].CacheControl = cc
	return true
}

// addCacheBreakpoints adds cache_control markers to the request components
// based on the configured PromptCachingConfig (conduit-3dru). The master
// switch gates everything; granular flags gate each breakpoint type.
//
// conduit-31jg.14: at most maxCacheBreakpoints markers, placed as
//  1. last tool definition (CacheTools)
//  2. systemBlocks[staticEnd] — the last byte-stable system block; dynamic
//     blocks after it (timestamp etc.) are outside the cached prefix
//  3. the LAST message — rolling breakpoint, so each tool-loop round trip
//     reads everything the previous one wrote (CacheHistory)
//  4. an anchor HistoryBreakpointInterval messages back, a second read
//     point when a round adds more than the ~20-block lookback (CacheHistory)
//
// Each is placed only when the estimated prefix up to it (tools → system →
// messages, in API order) reaches the model's minimum cacheable length.
// The slices' elements are modified in place.
func (a *AnthropicProvider) addCacheBreakpoints(
	tools []models.AnthropicTool,
	systemBlocks []models.ContentBlock,
	staticEnd int,
	messages []models.Message,
	model string,
) {
	if !a.caching.Enabled {
		return
	}
	minTokens := GetCacheMinTokens(model)
	cacheControl := &models.CacheControl{Type: "ephemeral"}
	if a.caching.ExtendedTTL {
		cacheControl.TTL = "1h"
	}

	used := 0
	canMark := func() bool { return used < maxCacheBreakpoints }

	// prefix is the running estimate of everything before the next marker.
	prefix := 0

	// Breakpoint 1: last tool definition.
	if len(tools) > 0 {
		prefix += estimateJSONTokens(tools)
		if a.caching.CacheTools && prefix >= minTokens && canMark() {
			tools[len(tools)-1].CacheControl = cacheControl
			used++
		}
	}

	// Breakpoint 2: last static system block.
	for i := range systemBlocks {
		prefix += estimateTokens(systemBlocks[i].Text)
		if i == staticEnd && a.caching.CacheSystem && prefix >= minTokens && canMark() {
			systemBlocks[i].CacheControl = cacheControl
			used++
		}
	}

	if !a.caching.CacheHistory || len(messages) == 0 {
		return
	}

	// Breakpoints 3 and 4: conversation history.
	msgPrefix := make([]int, len(messages)) // estimated prefix through message i
	running := prefix
	for i, msg := range messages {
		running += estimateMessageTokens(msg)
		msgPrefix[i] = running
	}

	last := len(messages) - 1
	if msgPrefix[last] >= minTokens && canMark() {
		if markMessageBreakpoint(&messages[last], cacheControl) {
			used++
		}
	}

	interval := a.caching.HistoryBreakpointInterval
	if interval <= 0 {
		interval = 6
	}
	anchor := last - interval
	if anchor >= 0 && msgPrefix[anchor] >= minTokens && canMark() {
		if markMessageBreakpoint(&messages[anchor], cacheControl) {
			used++
		}
	}
}
