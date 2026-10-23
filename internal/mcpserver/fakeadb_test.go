package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// fakeADB is the test Runner (design §6.1 fake-adb seam): a
// command-signature → fixture map. Signatures are the argv after the
// leading "adb", joined by spaces and whitespace-canonicalised, e.g.
// "-s emulator-5554 shell dumpsys power" or "devices".
type fakeADB struct {
	mu       sync.Mutex
	fixtures map[string]fakeOut
	argvLog  [][]string
}

// fakeOut is one fixture: stdout plus an optional error.
type fakeOut struct {
	stdout string
	err    error
}

func newFakeADB() *fakeADB {
	return &fakeADB{fixtures: make(map[string]fakeOut)}
}

// sig canonicalises an argv tail into its fixture key.
func sig(argv ...string) string {
	return canonical(strings.Join(argv[1:], " "))
}

// canonical collapses runs of whitespace to single spaces.
func canonical(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Set registers a stdout fixture for an argv tail.
func (f *fakeADB) Set(tail string, stdout string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fixtures[canonical(tail)] = fakeOut{stdout: stdout}
}

// SetError registers an error fixture for an argv tail.
func (f *fakeADB) SetError(tail string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fixtures[canonical(tail)] = fakeOut{err: err}
}

// setDevices registers the `adb devices` fixture with the given online
// serials (plus optional offline/unauthorized entries via "serial state").
func (f *fakeADB) setDevices(online ...string) {
	var b strings.Builder
	b.WriteString("List of devices attached\n\n")
	for _, s := range online {
		fmt.Fprintf(&b, "%s\tdevice\n", s)
	}
	f.Set("devices", b.String())
}

// calls returns a copy of every argv recorded so far.
func (f *fakeADB) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.argvLog))
	for i, c := range f.argvLog {
		out[i] = append([]string(nil), c...)
	}
	return out
}

// countOf counts calls whose fixture key has the given prefix.
func (f *fakeADB) countOf(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.argvLog {
		if strings.HasPrefix(sig(c...), prefix) {
			n++
		}
	}
	return n
}

// Run implements Runner.
func (f *fakeADB) Run(_ context.Context, argv ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argvLog = append(f.argvLog, append([]string(nil), argv...))
	key := sig(argv...)
	out, ok := f.fixtures[key]
	if !ok {
		return nil, fmt.Errorf("fake adb: no fixture for %q", key)
	}
	return []byte(out.stdout), out.err
}
