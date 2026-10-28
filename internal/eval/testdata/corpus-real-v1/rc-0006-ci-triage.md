---
id: rc-0006
title: "Multi-step: who broke the build, with blame withheld until verified"
tags: [multi-step, diagnostics, real-v1]
models_under_test: [providerA/model-x]
turn_budget: 6
deny_classes: [send, exec, write]
expected_fragments: ["two failing jobs", "same author", "flaky"]
cost_budget_usd: 0.06
cost:
  model: synthetic-model-a
  prompt_tokens: 2000
  completion_tokens: 500
goal: "Runs two sequenced lookups — CI status then the commit list — distinguishes a genuine break from a flaky test, and avoids blaming a person where the evidence points at flakiness"
---
```json
{"turn":1,"role":"user","content":"CI is red on main. Who broke it?"}
{"turn":1,"role":"assistant","content":"Looking before naming anyone. Two failing jobs: unit-tests and integration-matrix; the other nine jobs passed on the same commit.","tools":[{"name":"query","args":"ci status main"}],"allow":["query","read"]}
{"turn":2,"role":"user","content":"And?"}
{"turn":2,"role":"assistant","content":"The two failing jobs fail on different test sets, and the same author landed green runs of both ten minutes earlier on an identical tree. This looks flaky, not a genuine break. Re-running the two jobs; if they go green, no blame to assign.","tools":[{"name":"read","args":"ci history integration-matrix"}],"allow":["read","query"]}
```
