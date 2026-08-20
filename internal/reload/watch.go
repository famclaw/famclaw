package reload

import (
	"fmt"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultWatchDebounce is the coalescing window between a config-file change
// and the reload event it produces. Fast successive edits (an editor saving
// incrementally, a temp-write + rename pair) collapse into one reload.
const DefaultWatchDebounce = 500 * time.Millisecond

// ConfigWatcher watches a config file path and reports coalesced change
// events. Unlike a raw fsnotify.Write-only loop, it also reports atomic
// replacements — write-to-temp + rename, which is what config.Save and most
// editors and deployment tools use. Those saves surface on Linux (inotify)
// as Remove events on the old inode and create a new, unwatched inode at
// the same path; on macOS (kqueue) the watch follows the old vnode. After
// every delivered event the watcher re-adds the path so the fresh file
// stays in view.
//
// The Events channel has capacity 1: a delivery coalesces with whatever
// change follows it until the consumer's reload finishes, so bursts of
// events never flood the reload loop.
type ConfigWatcher struct {
	path     string
	debounce time.Duration

	notify    chan struct{}
	closed    chan struct{}
	done      chan struct{} // closed when the internal loop has exited
	closeOnce sync.Once

	mu sync.Mutex
	fw *fsnotify.Watcher
}

// NewConfigWatcher starts watching path. The path must exist at creation
// time.
func NewConfigWatcher(path string, debounce time.Duration) (*ConfigWatcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	if err := fw.Add(path); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("watching config %s: %w", path, err)
	}
	if debounce <= 0 {
		debounce = DefaultWatchDebounce
	}
	w := &ConfigWatcher{
		path:     path,
		debounce: debounce,
		notify:   make(chan struct{}, 1),
		closed:   make(chan struct{}),
		done:     make(chan struct{}),
		fw:       fw,
	}
	go w.loop()
	return w, nil
}

// Events yields a value after each coalesced config-file change. It is not
// closed by Close; consumers that must observe shutdown select on their
// own lifecycle context alongside Events.
func (w *ConfigWatcher) Events() <-chan struct{} { return w.notify }

// Close stops the watcher and waits for its internal loop to exit.
// Safe to call more than once.
func (w *ConfigWatcher) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	w.mu.Lock()
	fw := w.fw
	w.fw = nil
	w.mu.Unlock()
	if fw != nil {
		_ = fw.Close()
	}
	<-w.done
	return nil
}

// related reports whether the event means the config path changed. Any of
// Write/Create/Rename/Remove is treated as a candidate: the consumer
// re-loads, re-validates, and only applies a config that parses and
// validates, so a spurious event costs at most one failed load.
func related(op fsnotify.Op) bool {
	return op&fsnotify.Write != 0 ||
		op&fsnotify.Create != 0 ||
		op&fsnotify.Rename != 0 ||
		op&fsnotify.Remove != 0
}

func (w *ConfigWatcher) loop() {
	defer close(w.done)
	var timer *time.Timer
	for {
		select {
		case <-w.closed:
			if timer != nil {
				timer.Stop()
			}
			return
		case ev, ok := <-w.eventCh():
			if !ok {
				return
			}
			if !related(ev.Op) {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(w.debounce, w.fire)
		}
	}
}

// eventCh returns the current underlying fsnotify channel, or a nil channel
// (never ready) once the watcher is closed — leaving the loop's select on
// the closed case alone.
func (w *ConfigWatcher) eventCh() <-chan fsnotify.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fw == nil {
		return nil
	}
	return w.fw.Events
}

// fire is the debounced delivery. It re-adds the path first: the event
// that triggered it may have left a freshly-renamed file in place that the
// old inode watch no longer covers.
func (w *ConfigWatcher) fire() {
	w.readd()
	select {
	case w.notify <- struct{}{}:
	case <-w.closed:
	}
}

// readd ensures the path is watched again. Re-adding an already-watched
// path reports an error from fsnotify, which is ignored here; a transient
// "file does not exist" (mid-rename) gets another chance on the next
// event.
func (w *ConfigWatcher) readd() {
	w.mu.Lock()
	fw := w.fw
	w.mu.Unlock()
	if fw == nil {
		return
	}
	_ = fw.Add(w.path)
}
