package ai

import (
	"encoding/json"
	"log"
	"sort"
	"unicode/utf8"
)

// Tool-loop context guard (conduit-31jg.18(b)).
//
// trimRequestToFitContext (messages.go) handles the FIRST request of a turn:
// [system, history..., user]. Inside the tool loop the shape is
// [system, history..., user, (assistant+tool_calls, tool results...)..., ...]
// and a naive "drop oldest messages" would split an assistant tool_use from
// its tool_result blocks, which the Anthropic API rejects. fitRequestToWindow
// trims in whole units instead.

const (
	guardCharsPerToken = 4 // same estimate as trimRequestToFitContext
	// guardAttachmentChars approximates an image attachment's prompt cost.
	guardAttachmentChars = 1600 * guardCharsPerToken
	// guardMinToolResultChars is the floor a tool result is truncated to
	// when dropping whole units is not enough.
	guardMinToolResultChars = 2000
)

const toolResultTrimMarker = "\n\n[... tool result truncated to fit the model context window (conduit-31jg.18)]"

func estimateChatMessageChars(m ChatMessage) int {
	n := len(m.Role) + len(m.Content) + 10
	for _, tc := range m.ToolCalls {
		n += len(tc.ID) + len(tc.Name) + 20
		if b, err := json.Marshal(tc.Args); err == nil {
			n += len(b)
		}
	}
	n += len(m.Attachments) * guardAttachmentChars
	return n
}

// msgUnit is a run of messages that must be kept or dropped together: an
// assistant message with tool calls plus the tool results that follow it,
// or a single other message.
type msgUnit struct {
	start, end int // [start, end)
	chars      int
	system     bool
	round      bool
}

func buildMsgUnits(msgs []ChatMessage) []msgUnit {
	var units []msgUnit
	for i := 0; i < len(msgs); {
		u := msgUnit{start: i, system: msgs[i].Role == "system"}
		u.chars = estimateChatMessageChars(msgs[i])
		j := i + 1
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			u.round = true
			for j < len(msgs) && msgs[j].Role == "tool" {
				u.chars += estimateChatMessageChars(msgs[j])
				j++
			}
		}
		u.end = j
		units = append(units, u)
		i = j
	}
	return units
}

// fitRequestToWindow returns req unchanged when its estimated size fits the
// window, otherwise a shallow copy with a trimmed Messages slice (the
// caller's request and backing array are never mutated). Trim order:
//
//  1. drop whole units of prior history (before the turn's user message),
//     oldest first;
//  2. drop whole middle tool rounds (after the user message, before the
//     latest round), oldest first — tool_use/tool_result pairs stay intact;
//  3. truncate the largest remaining tool results to a floor.
//
// System messages, the turn's user message, and the latest tool round with
// everything after it are always kept.
func fitRequestToWindow(req *GenerateRequest, window int) *GenerateRequest {
	if req == nil || len(req.Messages) < 2 || window <= 0 {
		return req
	}
	outputReserve := req.MaxTokens
	if outputReserve <= 0 {
		outputReserve = defaultChainMaxTokens
	}
	toolChars := 0
	for _, t := range req.Tools {
		toolChars += len(t.Name) + len(t.Description) + 200
	}
	budget := (window-outputReserve)*guardCharsPerToken - toolChars
	if budget <= 0 {
		return req
	}

	units := buildMsgUnits(req.Messages)
	total := 0
	for _, u := range units {
		total += u.chars
	}
	if total <= budget {
		return req
	}

	firstRound, lastRound := -1, -1
	for i, u := range units {
		if u.round {
			if firstRound < 0 {
				firstRound = i
			}
			lastRound = i
		}
	}
	// Protected tail: the latest round and everything after it; with no
	// tool rounds, just the last message.
	tailStart := len(units) - 1
	if lastRound >= 0 {
		tailStart = lastRound
	}
	// Anchor: the turn's user message (last user unit before the first
	// round); with no rounds the tail already holds it.
	anchor := -1
	if firstRound >= 0 {
		for i := firstRound - 1; i >= 0; i-- {
			if !units[i].system && req.Messages[units[i].start].Role == "user" {
				anchor = i
				break
			}
		}
	}

	// Droppable units in priority order: history before the anchor, then
	// middle units between the anchor and the tail.
	var order []int
	for i := 0; i < tailStart; i++ {
		if units[i].system || i == anchor || (anchor >= 0 && i > anchor) {
			continue
		}
		order = append(order, i)
	}
	for i := anchor + 1; anchor >= 0 && i < tailStart; i++ {
		if !units[i].system {
			order = append(order, i)
		}
	}

	dropped := make(map[int]bool)
	droppedMsgs := 0
	for _, i := range order {
		if total <= budget {
			break
		}
		dropped[i] = true
		total -= units[i].chars
		droppedMsgs += units[i].end - units[i].start
	}

	kept := make([]ChatMessage, 0, len(req.Messages)-droppedMsgs)
	for i, u := range units {
		if !dropped[i] {
			kept = append(kept, req.Messages[u.start:u.end]...)
		}
	}

	// Still over: truncate the largest tool results (copy-on-write — kept
	// shares Message values, not backing storage, so edits are local).
	truncated := 0
	if total > budget {
		var toolIdx []int
		for i, m := range kept {
			if m.Role == "tool" && len(m.Content) > guardMinToolResultChars {
				toolIdx = append(toolIdx, i)
			}
		}
		sort.SliceStable(toolIdx, func(a, b int) bool { return len(kept[toolIdx[a]].Content) > len(kept[toolIdx[b]].Content) })
		for _, i := range toolIdx {
			if total <= budget {
				break
			}
			over := total - budget
			content := kept[i].Content
			newLen := len(content) - over - len(toolResultTrimMarker)
			if newLen < guardMinToolResultChars {
				newLen = guardMinToolResultChars
			}
			for newLen > 0 && !utf8.RuneStart(content[newLen]) {
				newLen--
			}
			kept[i].Content = content[:newLen] + toolResultTrimMarker
			total -= len(content) - len(kept[i].Content)
			truncated++
		}
	}

	out := *req
	out.Messages = kept
	log.Printf("[Router] Context guard: dropped %d messages, truncated %d tool results to fit %d-token window for model %q (~%d tokens after trim, fits=%v) (conduit-31jg.18)",
		droppedMsgs, truncated, window, req.Model, (total+toolChars)/guardCharsPerToken, total <= budget)
	return &out
}
