package agent

// buildMemorySection returns memory recall instructions if memory tools are available
func buildMemorySection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	hasMemorySearch := params.AvailableTools["MemorySearch"]
	if !hasMemorySearch {
		return ""
	}

	return `## Memory Recall
Before answering anything about prior work, decisions, dates, people, preferences, or todos: run MemorySearch to find relevant content across MEMORY.md, memory/*.md, and session history.
If you need the full file content (e.g., before modifying), use Read instead — MemorySearch is for finding, Read is for reading.
Citations: include Source: <path#line> when it helps the user verify memory snippets.
`
}

// buildMemoryPersistenceSection returns instructions for writing to memory files
func buildMemoryPersistenceSection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	hasWriteTool := params.AvailableTools["Write"]
	hasBashTool := params.AvailableTools["Bash"]
	if !hasWriteTool && !hasBashTool {
		return ""
	}

	return `## Memory Persistence
You have no persistent memory between sessions — files are your brain. Write things down or lose them.

**When to write:**
- Decisions made, preferences learned, facts discovered
- Lessons from mistakes (so you don't repeat them)
- Anything the user says to remember

**Where to write:**
- ` + "`memory/YYYY-MM-DD.md`" + ` — daily logs, raw notes, in-the-moment capture
- ` + "`MEMORY.md`" + ` — curated long-term memory, distilled wisdom

**How to write:**
- **Append new entries:** Use Bash with ` + "`echo \"...\" >> file`" + ` — no need to read first
- **Modify existing content:** Read the file once, make changes, Write the full content back
- **Never read the same file multiple times** in one operation — read once, then write

**Memory hygiene:** Periodically review recent daily files and promote important insights to MEMORY.md. To consolidate: Read the file, reorganize/dedupe mentally, Write it back clean.
`
}

// buildBrainSection returns cognitive architecture instructions if the Brain tool is available.
func buildBrainSection(params *SectionParams) string {
	if params.IsMinimal || !params.AvailableTools["Brain"] {
		return ""
	}

	return `## Brain (Cognitive Architecture)
You have a tiered memory system beyond the context window. USE IT.

### Lookup-First Pattern
Before reading any file for a fact you've accessed before this session:
1. ` + "`Brain(action=\"get\", key=\"likely.key.name\")`" + ` — check if it's cached
2. Hit? Use it. Done. Miss? Read the file, then cache the key fact:
   ` + "`Brain(action=\"store\", key=\"solar.panel_count\", value=\"24\", tier=\"working\")`" + `

### What Goes Where

| Tier | What | Examples | Lifetime |
|------|------|----------|----------|
| **longterm** | Core facts, stable preferences, learned patterns | jeff.birthday, solar.panel_count, pets.rex.breed | Survives restarts |
| **working** | Session-extracted facts, current task state | solar.today.production, email.unread_count | Session only (promote if important) |
| **scratch** | Intermediate calculations, temp values | push/pop only | Seconds |

### Key Naming Convention
Use dot-separated namespaces: ` + "`domain.subject.attribute`" + `
- ` + "`jeff.birthday`" + `, ` + "`jeff.favorite_color`" + `
- ` + "`solar.today.production`" + `, ` + "`solar.panel_count`" + `
- ` + "`pets.rex.breed`" + `, ` + "`session.current_topic`" + `

### When to Store
- After reading a file: cache the 2-3 key facts you extracted
- After a tool call returns useful data: cache the summary
- When the user states a fact or preference: store immediately
- When you compute something you might need again: working memory

### When to Promote (working → longterm)
- Facts true across sessions (birthdays, counts, preferences)
- Learned patterns ("user prefers X over Y")
- Infrastructure facts ("the NAS has 4 drive bays")

### When NOT to Store
- Entire file contents (that's what files are for)
- Conversational context (that's what the context window is for)
- One-time responses (just respond, don't cache)

### Consolidation
At session end or handoff, call ` + "`Brain(action=\"consolidate\")`" + ` to auto-promote high-salience working memory to longterm, flush changes to disk, and report what was promoted/evicted.

### Searching
` + "`Brain(action=\"recall\", query=\"solar\")`" + ` searches all tiers by key name and value content. Results return with tier and salience so you know how fresh/reliable each fact is.
`
}
