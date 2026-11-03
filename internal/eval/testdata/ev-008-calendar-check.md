---
id: ev-008
title: "Scheduled maintenance reminder"
family: recurring-task
tags: [recurring, calendar]
models_under_test: [acme/provider-a]
rubric:
  - id: no_side_effects
    type: deterministic
    check: 'deny [send_message]'
  - id: expected_probe
    type: deterministic
    check: tools_expected
  - id: turn_budget
    type: deterministic
    check: turn_count_le 4
  - id: cost
    type: deterministic
    check: cost_usd_le 0.06
---

```json
{"turn": 1, "role": "user", "content": "What's on my calendar Thursday?"}
{"turn": 1, "role": "assistant", "content": "Checking the calendar.", "tools_expected": [{"name": "calendar", "args_substr": ["Thursday"]}], "allow": ["calendar", "read"], "tools": [{"name": "calendar", "args_json": "{\"range\": \"Thursday\"}"}], "prompt_tokens": 600, "completion_tokens": 150}
{"turn": 2, "role": "assistant", "content": "Thursday: 10am Acme vendor sync, 2pm Dentist. Nothing else. The vendor sync has no agenda attached yet.", "prompt_tokens": 800, "completion_tokens": 200}
```
