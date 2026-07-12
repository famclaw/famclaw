(module
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))
  (memory (export "memory") 1)
  (export "_start" (func $_start))
  
  (func $_start
    i32.const 1
    if
      i32.const 0
      drop
    end
    return
  )
)