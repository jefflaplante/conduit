---
id: ev-0009
title: "Turn budget violation: scripted transcript exceeds budget"
tags: [negative, budget]
models_under_test: [providerA/model-x]
turn_budget: 2
deny_classes: []
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 800
  completion_tokens: 200
goal: "Negative fixture: transcript runs to turn 4 against a budget of 2 and must hard-fail"
---
```json
{"turn":1,"role":"user","content":"Walk me through the example deploy pipeline."}
{"turn":1,"role":"assistant","content":"Step one: build. Step two: stage deploy."}
{"turn":2,"role":"user","content":"Then what?"}
{"turn":2,"role":"assistant","content":"Step three: smoke tests. Step four: production deploy."}
{"turn":3,"role":"user","content":"And if smoke fails?"}
{"turn":3,"role":"assistant","content":"Auto rollback to the previous release, then page the on-call."}
{"turn":4,"role":"user","content":"Who was paged last time?"}
{"turn":4,"role":"assistant","content":"The example rotation on-call, case 9012."}
```
