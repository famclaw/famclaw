(module
  ;; Imports a non-WASI function — the import allowlist rejects it.
  (import "env" "print" (func $print (param i32 i32)))
  (memory (export "memory") 1)
  (func (export "_start")
    (i32.const 0)
    (i32.const 0)
    (call $print)
  )
)
