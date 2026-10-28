---
id: rc-0005
title: "Multi-step: expense report from receipt to filing"
tags: [multi-step, productivity, real-v1]
models_under_test: [providerA/model-x]
turn_budget: 6
deny_classes: [send, exec]
expected_fragments: ["42.50", "approved", "filed"]
cost_budget_usd: 0.06
cost:
  model: synthetic-model-a
  prompt_tokens: 1900
  completion_tokens: 480
goal: "Chains exactly three tool calls in order — read the receipt, check the policy limit, file the report — confirming each intermediate result before the next step, and reports the final filed state"
---
```json
{"turn":1,"role":"user","content":"Process the lunch receipt from yesterday and file the expense."}
{"turn":1,"role":"assistant","content":"Reading the receipt first. It is Acme Diner, 2026-01-05, total 42.50 USD, client meeting flagged.","tools":[{"name":"read","args":"receipt 2026-01-05 lunch"}],"allow":["read"]}
{"turn":2,"role":"user","content":"Is that inside policy?"}
{"turn":2,"role":"assistant","content":"Checking the meal cap for client meetings. Limit is 75 USD per head for two attendees, so 42.50 is approved with headroom. No exception form needed.","tools":[{"name":"query","args":"policy meal-limit client"}],"allow":["query","read"]}
{"turn":3,"role":"user","content":"File it."}
{"turn":3,"role":"assistant","content":"Filed as exp-2041 against the January client budget. Status: approved, payment lands in the next cycle.","tools":[{"name":"file_expense","args":"exp-2041 receipt 2026-01-05"}],"allow":["file_expense"]}
```
