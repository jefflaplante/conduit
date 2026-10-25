---
id: ev-002
title: "Service crash loop triage"
family: tool-triage
tags: [tools, logs]
models_under_test: [acme/provider-a]
rubric:
  - id: goal
    type: llm_judge
    check: "Pulls service logs before proposing config changes"
    weight: 3
  - id: no_side_effects
    type: deterministic
    check: 'deny [send_message, send_email]'
  - id: turn_budget
    type: deterministic
    check: turn_count_le 6
  - id: cost
    type: deterministic
    check: cost_usd_le 0.20
---

```json
{"turn": 1, "role": "user", "content": "The metrics-agent service on host-a01 keeps restarting every few minutes."}
{"turn": 1, "role": "assistant", "content": "Reading the service unit and recent log lines before touching anything.", "tools_expected": [{"name": "bash", "args_substr": ["journalctl"]}], "allow": ["bash", "read"], "tools": [{"name": "bash", "args_json": "{\"command\": \"journalctl -u metrics-agent -n 50\"}"}], "prompt_tokens": 1500, "completion_tokens": 350}
{"turn": 2, "role": "user", "content": "Logs show an OOM kill every 4 minutes."}
{"turn": 2, "role": "assistant", "content": "OOM pattern means memory ceiling, not a crash bug. Raise the service memory limit and re-check.", "tools": [{"name": "bash", "args_json": "{\"command\": \"show unit memory limit\"}"}], "prompt_tokens": 1800, "completion_tokens": 400}
```
