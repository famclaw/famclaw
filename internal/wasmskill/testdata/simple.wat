(module
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))
  (import "wasi_snapshot_preview1" "fd_read"
    (func $fd_read (param i32 i32 i32 i32) (result i32)))
  (memory (export "memory") 1)
  (export "_start" (func $_start))
  
  (func $_start
    ;; Success!
    i32.const 0
    return
  )
)