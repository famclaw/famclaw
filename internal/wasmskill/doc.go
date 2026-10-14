// Package wasmskill runs first-party Wasm skill servers as WASI command
// modules (wazero), giving the host a uniform, sandboxed runtime for
// deterministic skill logic.
//
// A skill is a Wasm module that exposes one JSON-RPC 2.0 endpoint over
// stdin/stdout, one NDJSON message per line:
//
//	host -> guest:  {"jsonrpc":"2.0","id":N,"method":"skill_init"|"skill_invoke"|"skill_cleanup","params":{...}}
//	guest -> host:  {"jsonrpc":"2.0","id":N,"result":{...}} | {"jsonrpc":"2.0","id":N,"error":{...}}
//
// The host enforces the security boundary; the guest is untrusted:
//
//   - Deny-by-default filesystem: a module gets no preopened directory
//     unless Config.SandboxRoot is set, in which case exactly one directory
//     is preopened, mounted at "/". wazero's preopen path resolution rejects
//     absolute and ".." paths, so the guest cannot name a file outside the
//     root; there is no escape.
//   - Import allowlist: only functions in the "wasi_snapshot_preview1"
//     module may be imported. No clock, no random, no socket - a Wasm skill
//     has no network access.
//   - wasm-or-refuse: policy.Evaluate (Evaluate) decides from the skill
//     manifest whether a skill may run at all; only the "wasm" tier reaches
//     this package.
//   - Bounded lifetime: NewSkill waits for the init response with a
//     deadline; Invoke runs with a deadline (or the caller's) and kills the
//     module when it expires; Close is always finite.
//
// This package is the P2 runtime. It is not wired into the live tool path
// (P3): nothing in internal/skillbridge or internal/agent imports it yet.
package wasmskill
