(module
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))
  (import "wasi_snapshot_preview1" "fd_read"
    (func $fd_read (param i32 i32 i32 i32) (result i32)))
  (memory (export "memory") 1)
  (export "_start" (func $_start))
  
  (func $_start
    ;; Buffer for reading/writing
    (local $buf i32)
    (local $buf_len i32)
    (local $iovecs i32)
    (local $result i32)
    (local $nread i32)
    (local $nwritten i32)
    
    ;; Allocate memory for our buffer (64 bytes should be plenty for echo)
    i32.const 64
    local.set $buf
    
    ;; Set up iovec for fd_read: [buffer_ptr, buffer_len]
    i32.const 80  ;; iovecs start at offset 80
    local.set $iovecs
    
    local.get $iovecs
    local.get $buf
    i32.store       ;; iovecs[0] = buffer_ptr
    
    local.get $iovecs
    i32.const 4
    i32.add
    i32.const 64   ;; buffer_len = 64
    i32.store       ;; iovecs[1] = buffer_len
    
    ;; Call fd_read(fd=0, iovecs=80, iovecs_len=1, nread=offset_84)
    i32.const 0     ;; fd = 0 (stdin)
    local.get $iovecs
    i32.const 1     ;; iovecs_len = 1
    i32.const 84    ;; offset for nread result
    call $fd_read
    local.set $result
    
    ;; Check if read was successful (return value == 0, which is SUCCESS)
    i32.const 0
    local.get $result
    i32.eq
    if (then
      ;; Get the number of bytes read from offset 84
      i32.const 84
      i32.load
      local.set $nread
      
      ;; Set up iovec for fd_write: [buffer_ptr, nread]
      local.get $iovecs
      local.get $buf
      i32.store       ;; iovecs[0] = buffer_ptr (same buffer)
      
      local.get $iovecs
      i32.const 4
      i32.add
      local.get $nread  ;; nread from fd_read
      i32.store         ;; iovecs[1] = nread (only write what we read)
      
      ;; Call fd_write(fd=1, iovecs=80, iovecs_len=1, nwritten=offset_88)
      i32.const 1     ;; fd = 1 (stdout)
      local.get $iovecs
      i32.const 1     ;; iovecs_len = 1
      i32.const 88    ;; offset for nwritten result
      call $fd_write
      local.set $result
      
      ;; Check if write was successful
      i32.const 0
      local.get $result
      i32.eq
      if (then
        ;; Success!
        nop
      ) else
        ;; Write failed - trap
        i32.const 1
        call $fd_write  ;; This will trap with badf
      end
    ) else
      ;; Read failed - trap
      i32.const 1
      call $fd_read  ;; This will trap with badf
    end
  )
)