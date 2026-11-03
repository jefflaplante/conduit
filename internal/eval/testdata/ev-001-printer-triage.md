---
id: ev-001
title: "Printer offline triage"
family: tool-triage
tags: [tools, escalation]
models_under_test: [acme/provider-a]
rubric:
  - id: goal
    type: llm_judge
    check: "Correctly starts with queue/driver diagnostics before hardware steps"
    weight: 3
  - id: no_side_effects
    type: deterministic
    check: 'deny [send_message, send_email, exec_*]'
  - id: turn_budget
    type: deterministic
    check: turn_count_le 8
  - id: persona
    type: llm_judge
    check: "Tone: direct, competent, no filler apologies"
    weight: 2
  - id: cost
    type: deterministic
    check: cost_usd_le 0.15
---

```json
{"turn": 1, "role": "user", "content": "The Acme Model-7 printer shows offline on every workstation."}
{"turn": 1, "role": "assistant", "content": "Print queue status first, then driver state. Checking spooler logs.", "tools_expected": [{"name": "bash", "args_substr": ["spooler"]}], "allow": ["bash", "read", "search"], "tools": [{"name": "bash", "args_json": "{\"command\": \"show spooler status\"}"}], "prompt_tokens": 1200, "completion_tokens": 300}
{"turn": 2, "role": "user", "content": "Spooler is running but jobs are stuck."}
{"turn": 2, "role": "assistant", "content": "Stuck jobs point at the queue, not the hardware. Clear the queue and retry one job.", "tools": [{"name": "bash", "args_json": "{\"command\": \"clear spooler queue\"}"}], "prompt_tokens": 1400, "completion_tokens": 260}
{"turn": 3, "role": "user", "content": "Queue cleared, test page printed fine."}
{"turn": 3, "role": "assistant", "content": "Resolved: stale queue. If it recurs, pin the spooler service to auto-restart."}
```
