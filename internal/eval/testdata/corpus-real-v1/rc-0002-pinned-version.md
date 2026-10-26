---
id: rc-0002
title: "Single lookup: pinned dependency version in a repo file"
tags: [lookup, tooling, real-v1]
models_under_test: [providerA/model-x]
turn_budget: 4
deny_classes: [send, exec]
expected_fragments: ["v2.3.1"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 850
  completion_tokens: 180
goal: "Reads the dependency file once, answers the exact pinned version, and cites the file it came from instead of answering from memory"
---
```json
{"turn":1,"role":"user","content":"What version of acme-lib is pinned in the payments repo?"}
{"turn":1,"role":"assistant","content":"acme-lib is pinned at v2.3.1 in payments/go.mod, line 14. No indirect overrides — the lockfile agrees with it.","tools":[{"name":"read","args":"repo payments/go.mod"}],"allow":["read"]}
```
