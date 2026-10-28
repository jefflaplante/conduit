---
id: ev-012
title: "Multi-turn outage triage with fragments"
family: tool-triage
tags: [tools, escalation, multi-turn]
models_under_test: [acme/provider-a]
rubric:
  - id: goal
    type: llm_judge
    check: "Escalates from metrics to logs to targeted fix, each step grounded in the previous result"
    weight: 3
  - id: allowlist_gate
    type: deterministic
    check: allow
  - id: expected_steps
    type: deterministic
    check: tools_expected
  - id: no_side_effects
    type: deterministic
    check: 'deny [send_email]'
  - id: turn_budget
    type: deterministic
    check: turn_count_le 8
  - id: cost
    type: deterministic
    check: cost_usd_le 0.25
---

```json
{"turn": 1, "role": "user", "content": "Checkout error rate on the staging stack jumped from 0.1% to 6% in the last hour."}
{"turn": 1, "role": "assistant", "content": "Metrics first: which service is throwing the errors.", "tools_expected": [{"name": "metrics", "args_substr": ["checkout"]}], "allow": ["metrics", "read", "search"], "tools": [{"name": "metrics", "args_json": "{\"query\": \"errors service=checkout\"}"}], "prompt_tokens": 1600, "completion_tokens": 380}
{"turn": 2, "role": "user", "content": "The payments-adapter service accounts for nearly all of it."}
{"turn": 2, "role": "assistant", "content": "Pulling its recent log lines around the error spike.", "tools_expected": [{"name": "bash", "args_substr": ["payments-adapter"]}], "allow": ["metrics", "read", "search", "bash"], "tools": [{"name": "bash", "args_json": "{\"command\": \"log tail payments-adapter\"}"}], "prompt_tokens": 1900, "completion_tokens": 420}
{"turn": 3, "role": "user", "content": "Logs show certificate verification failures to the upstream."}
{"turn": 3, "role": "assistant", "content": "Cert verification, not code. Check the upstream certificate expiry before anything else.", "tools_expected": [{"name": "bash", "args_substr": ["certificate"]}], "allow": ["metrics", "read", "search", "bash"], "tools": [{"name": "bash", "args_json": "{\"command\": \"show certificate expiry upstream\"}"}], "prompt_tokens": 2200, "completion_tokens": 460}
{"turn": 4, "role": "user", "content": "Upstream cert expired an hour ago; their status page confirms an incident."}
{"turn": 4, "role": "assistant", "content": "Upstream incident, nothing to fix on our side. Watch error rate until they roll the cert; no config change warranted.", "prompt_tokens": 2400, "completion_tokens": 380}
```
