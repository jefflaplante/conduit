---
id: ev-004
title: "Network latency report request"
family: data-summary
tags: [summary, no-tools]
models_under_test: [acme/provider-a]
rubric:
  - id: goal
    type: llm_judge
    check: "Summarizes latency trend and flags the weekly spike pattern"
    weight: 2
  - id: turn_budget
    type: deterministic
    check: turn_count_le 4
  - id: no_side_effects
    type: deterministic
    check: 'deny [send_message, send_email]'
  - id: cost
    type: deterministic
    check: cost_usd_le 0.05
---

```json
{"turn": 1, "role": "user", "content": "Summarize the branch office latency data I pasted: Mon 42ms, Tue 45ms, Wed 180ms, Thu 44ms, Fri 43ms."}
{"turn": 1, "role": "assistant", "content": "Baseline ~43-45ms with one outlier: Wednesday at 180ms, roughly 4x baseline. Worth checking what ran Wednesday; the rest of the week is flat and healthy.", "prompt_tokens": 800, "completion_tokens": 220}
```
