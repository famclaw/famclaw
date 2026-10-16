(module
  ;; Valid module with no _start export — the wasm-or-refuse gate rejects it.
  (memory (export "memory") 1)
)
