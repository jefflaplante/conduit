package ai

import (
	"sync"

	"conduit/internal/sessions"
)

// History trimming with hysteresis (conduit-31jg.63).
//
// Anthropic prompt caching matches the request prefix byte for byte. Once a
// session's history reached its budget, every trimming layer used to drop
// just enough of the oldest messages to fit — about one turn per turn — so
// the first history message changed on every request and every history
// cache breakpoint missed: the whole history was re-billed at the full or
// cache-write price each turn.
//
// Each layer now trims with hysteresis: when over budget it cuts down to a
// low-water mark (defaultHistoryLowWater of the budget) in whole units, and
// then leaves the prefix alone until the budget is exceeded again, which
// takes roughly (1-lowWater)*budget of new conversation.
//
//   - Router.getRecentMessagesTokenAware (history from the session store,
//     HistoryConfig.MaxTokens / MaxMessages): stateful. It remembers the
//     first kept message per session and reuses that cut while the tail
//     from it still fits.
//   - trimRequestToFitContext and fitRequestToWindow (context-window
//     guards): stateless. The drop point snaps to multiples of a chunk of
//     (1-lowWater)*budget measured from the start of their input, so a
//     stable input prefix with a growing tail gives the same drop point
//     until the tail has grown by about a chunk. Because the store layer
//     above keeps its prefix stable, these layers stay stable too.
//
// Units: a history cut only ever starts at a user message, so a user turn
// stays together with its assistant replies and an assistant tool_use with
// its tool_results. System messages and the current user message are never
// dropped.

// defaultHistoryLowWater is the fraction of a budget a trim cuts down to.
const defaultHistoryLowWater = 0.75

// historyLowWater returns f when it is a usable fraction, else the default.
func historyLowWater(f float64) float64 {
	if f <= 0 || f >= 1 {
		return defaultHistoryLowWater
	}
	return f
}

// hysteresisChunk is the snap granularity for a stateless trim of a budget.
func hysteresisChunk(budgetChars int) int {
	return int(float64(budgetChars) * (1 - defaultHistoryLowWater))
}

// snappedDropCount returns how many leading units to drop so the remaining
// units total at most avail chars. The amount dropped is rounded up to a
// multiple of chunk (cumulative chars from the first unit), so repeated
// calls with the same leading units and a growing tail return the same
// count until the overflow crosses the next chunk boundary. chunk <= 0
// drops the minimum.
func snappedDropCount(unitChars []int, avail, chunk int) int {
	total := 0
	for _, c := range unitChars {
		total += c
	}
	need := total - avail
	if need <= 0 {
		return 0
	}
	target := need
	if chunk > 0 {
		target = (need + chunk - 1) / chunk * chunk
	}
	cum := 0
	for i, c := range unitChars {
		if cum >= target {
			return i
		}
		cum += c
	}
	return len(unitChars)
}

// historyMsgChars is the store-layer size estimate of one message.
func historyMsgChars(m sessions.Message) int {
	return len(m.Content) + len(m.Role) + 10 // overhead for role/structure
}

// historyCutCache remembers, per session, the ID of the first history
// message sent to the model (conduit-31jg.63). Lost on restart: the first
// turn afterwards picks a fresh low-water cut.
type historyCutCache struct {
	mu   sync.Mutex
	cuts map[string]string
}

// maxHistoryCuts bounds the cache; when full it is reset (each session then
// re-cuts once).
const maxHistoryCuts = 4096

func (c *historyCutCache) get(sessionKey string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.cuts[sessionKey]
	return id, ok
}

func (c *historyCutCache) set(sessionKey, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cuts == nil || len(c.cuts) >= maxHistoryCuts {
		c.cuts = make(map[string]string)
	}
	c.cuts[sessionKey] = id
}

// selectHistoryWindow returns the index of the first message to send from
// messages (chronological, newest last), keeping the previous cut prevCut
// while the tail from it fits charBudget, and otherwise re-cutting down to
// the low-water mark of both the char budget and maxMessages.
//
// complete reports that messages holds the session's whole history (the
// fetch was not capped), so a fresh cut may keep everything when it fits.
func selectHistoryWindow(messages []sessions.Message, prevCut string, charBudget, maxMessages, minMessages int, lowWater float64, complete bool) int {
	n := len(messages)
	if n == 0 {
		return 0
	}
	suffix := make([]int, n+1) // suffix[i] = chars of messages[i:]
	for i := n - 1; i >= 0; i-- {
		suffix[i] = suffix[i+1] + historyMsgChars(messages[i])
	}

	if prevCut != "" {
		for i := n - 1; i >= 0; i-- {
			if messages[i].ID == prevCut {
				if suffix[i] <= charBudget {
					return i // prefix unchanged since the last trim
				}
				break
			}
		}
	} else if complete && suffix[0] <= charBudget {
		return 0
	}

	// Re-cut to the low-water mark.
	charTarget := int(float64(charBudget) * lowWater)
	countTarget := n
	if maxMessages > 0 {
		countTarget = int(float64(maxMessages) * lowWater)
	}
	start := n
	for i := n - 1; i >= 0; i-- {
		kept := n - i
		if kept <= minMessages || (suffix[i] <= charTarget && kept <= countTarget) {
			start = i
			continue
		}
		break
	}
	if start >= n {
		start = n - 1
	}

	// Align to a user turn so no reply (or tool round) is split from the
	// user message that started it.
	if messages[start].Role != "user" {
		j := start + 1
		for j < n && messages[j].Role != "user" {
			j++
		}
		if j < n && n-j >= minMessages {
			start = j
		} else {
			k := start - 1
			for k >= 0 && messages[k].Role != "user" {
				k--
			}
			if k >= 0 {
				start = k
			}
		}
	}
	return start
}

// turnGroups splits msgs[from:to] into keep/drop groups that each start at
// a user message (the first group may start with anything). Indices are
// relative to msgs. A "tool" message is never a group start, so an
// assistant tool_use always stays with its tool_results.
func turnGroups(msgs []ChatMessage, from, to int) [][2]int {
	var groups [][2]int
	for i := from; i < to; i++ {
		if i == from || msgs[i].Role == "user" {
			groups = append(groups, [2]int{i, i + 1})
			continue
		}
		groups[len(groups)-1][1] = i + 1
	}
	return groups
}
