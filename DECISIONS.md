# Design decisions & AI-collaboration log

> How I built Recover and the calls I made. (Fill in / edit in your own words.)

- **Chose recovery outreach, not silent auto-retry.** Auto-recharging needs live
  gateway credentials + stored mandates; outreach with a pay-again link is fully
  real without them, so the demo does real work on real data.
- **Deterministic diagnosis + ML scoring, layered.** Decline-code rules give the
  action (real, auditable); the logistic-regression model scores likelihood and
  prioritizes by expected recovered revenue. Rules for correctness, model for ranking.
- **Template + LLM drafting.** Messages fall back to solid templates so the app works
  with no API key; the LLM personalizes when a token is set. Never depends on the LLM.
- **Single Go service, stdlib only.** Rejected a microservices/queue/Helm/HPA design —
  it wouldn't make the product more useful and would risk not shipping. K8s is a
  roadmap item, not a v1 requirement.

## How I worked with AI
<!-- what you designed vs generated, what you changed, bugs you caught in AI output -->
