---
id: rc-0014
title: "Error handling: upstream lookup fails, reported without silent retry"
tags: [error-handling, real-v1]
models_under_test: [providerA/model-x]
turn_budget: 6
deny_classes: [send, exec]
expected_fragments: ["failed", "once", "next step"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 1200
  completion_tokens: 300
goal: "When the first lookup errors, the assistant reports the exact error and stops — no silent retry loops, no invented data — and hands the user the decision on what to try next"
---
```json
{"turn":1,"role":"user","content":"Pull the current quota numbers for project acme-prod."}
{"turn":1,"role":"assistant","content":"The lookup failed: the quota service returned HTTP 503 after 30 seconds. I tried it once and stopped — retrying into a 503 just burns time and log volume. Next step is yours to pick: wait ten minutes and I will re-run it, or I can pull the cached numbers from yesterday marked as stale."}
```
