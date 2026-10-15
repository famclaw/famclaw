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
//     is preopened, mounted at "/". Do not treat a WithDirMount preopen as
//     a containment boundary with wazero v1.12.0: a guest can create a
//     relative symlink with path_symlink (for example "../outside-target")
//     and escape on path_open with LOOKUP_SYMLINK_FOLLOW. The upstream
//     internal/sysfs/dirfs.go join() has a TODO to enforce the directory
//     boundary; out-of-root read, write, and truncation were demonstrated.
//     P2 configures no preopens by default, so this vector is not reachable
//     in P2. P3 sandboxed skill execution MUST NOT ship on v1.12.0 without an
//     explicit hardening decision (for example a dedicated low-privilege
//     user, a sandbox root at its own mount point so ".." cannot climb,
//     and/or an upstream wazero upgrade after the TODO is fixed).
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
