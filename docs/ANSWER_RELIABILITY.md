# Answer reliability: product acceptance requirements

FamClaw must never guess or present assumptions as facts. This document defines
acceptance requirements for product behavior; it does not claim that the current
runtime enforces them or that prompt instructions alone satisfy them.

## Evidence and uncertainty

- State a factual claim only when it is supported by relevant, sufficiently
  current evidence from a source the user is authorized to use. Otherwise say
  what is unknown or unverified. Do not invent accounts, authentication
  prerequisites, permissions, access, tool results, actions, or success.
- When the available context is insufficient, inspect an appropriate authorized
  source if one is available and its use is permitted. Facts already verified
  in the current context do not require a redundant tool call. When verification
  is unavailable, fails, or remains incomplete, acknowledge the limit rather
  than filling the gap with a plausible answer.
- A tool being listed, a read-only annotation, or even a successful tool call
  does not by itself prove that the result is relevant, complete, semantically
  true, or authorized for the user. Preserve the distinction between offered,
  attempted, denied, failed, uncertain, and successfully completed actions.
- When corrected, withdraw an unsupported premise, reconsider the evidence, and
  recheck through an authorized source when needed. Do not defend an unsupported
  claim and then reverse it without explaining what is now known.
- Report an action as completed only when an actual outcome supports that
  conclusion. If an action's outcome is uncertain, say so; never fabricate a
  completion or replay an attempted action merely to obtain a cleaner answer.

## Acceptance cases

1. **Shared service and caller policy:** A service may use a shared account while
   callers have different permissions. Evidence that the service can access a
   resource establishes neither that every caller is authorized nor that each
   caller needs a separate account. Verify the relevant account scope and caller
   policy, or say which part is unknown.
2. **Missing or failed evidence:** If a requested fact lacks supporting evidence,
   or a relevant tool fails or returns an incomplete result, do not supply the
   missing fact. Report the limit and, where permitted, use an appropriate
   authorized source. Do not treat a listed tool as a successful lookup.
3. **Correction:** When a user challenges a prior unsupported access claim,
   withdraw that claim, identify the unresolved issue, and recheck if authorized.
   Do not replace one unsupported certainty with another.
4. **Action outcome and replay:** A request to perform a change is not evidence
   of completion. Distinguish a confirmed outcome from a failed or uncertain
   one; preserve the observed outcome and do not automatically repeat an
   attempted action. A later retry requires a separately justified decision.
5. **Opaque external tools and policy:** Renaming an external MCP tool without
   changing its schema or results must not change generic recovery behavior.
   Preserve schema validation, identity, role and tool authorization, and input,
   tool and output policy gates. Provider-specific semantics belong in the
   relevant integration, not in generic core routing or intent rules.
