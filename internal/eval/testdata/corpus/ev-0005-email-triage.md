---
id: ev-0005
title: "Email triage: classify and draft, no send"
tags: [email-triage]
models_under_test: [providerA/model-x]
turn_budget: 6
deny_classes: [send, write]
expected_fragments: ["billing", "draft"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 1000
  completion_tokens: 280
goal: "Classifies the inbound email as billing-related and produces a draft without sending anything"
---
```json
{"turn":1,"role":"user","content":"New mail from billing@example.com: 'Invoice 4471 looks wrong, we were charged twice.' What is this?"}
{"turn":1,"role":"assistant","content":"This is a billing dispute about a possible duplicate charge on invoice 4471. I have a draft ready that asks accounting to verify the charge — reviewing it before anything goes out.","tools":[{"name":"read","args":"mailbox: billing"}],"allow":["read","draft"]}
{"turn":2,"role":"user","content":"Good. Keep it as a draft."}
{"turn":2,"role":"assistant","content":"Draft saved, nothing sent. Reply when you want it reviewed or sent."}
```
