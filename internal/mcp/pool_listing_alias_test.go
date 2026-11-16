package mcp

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	protocol "github.com/mark3labs/mcp-go/mcp"
)

func TestPoolListingsDoNotCopyToolsForEveryAlias(t *testing.T) {
	pool := NewPool("", false, false)
	mc := &managedClient{name: "server", client: &Client{tools: []protocol.Tool{protocol.NewTool("only")}}}
	pool.clients["server"] = mc
	pool.clients["only"] = mc
	for _, tc := range []struct {
		name string
		list func()
	}{{"names", func() { _ = pool.ListTools() }}, {"metadata", func() { _ = pool.ListToolInfos() }}} {
		t.Run(tc.name, func(t *testing.T) {
			base := testing.AllocsPerRun(5, tc.list)
			for i := 0; i < 80; i++ {
				pool.clients[fmt.Sprintf("stale-alias-%d", i)] = mc
			}
			many := testing.AllocsPerRun(5, tc.list)
			for i := 0; i < 80; i++ {
				delete(pool.clients, fmt.Sprintf("stale-alias-%d", i))
			}
			if many > base+12 {
				t.Fatalf("duplicate aliases amplified snapshot allocations: base %.0f, many %.0f", base, many)
			}
		})
	}
}

func TestPoolListingsPreserveOwnedToolsWhenServerKeyShadowed(t *testing.T) {
	pool := NewPool("", false, false)
	a := &managedClient{name: "server", client: &Client{tools: []protocol.Tool{protocol.NewTool("alpha"), protocol.NewTool("omega"), protocol.NewTool("not-routed")}}}
	b := &managedClient{name: "other", client: &Client{tools: []protocol.Tool{protocol.NewTool("server")}}}
	pool.clients["server"] = b // An alias can shadow a canonical server name.
	pool.clients["other"] = b
	pool.clients["alpha"] = a
	pool.clients["omega"] = a
	names := pool.ListTools()
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"alpha", "omega", "server"}) {
		t.Fatalf("names: %v", names)
	}
	infos := pool.ListToolInfos()
	got := map[string]string{}
	for _, info := range infos {
		got[info.Name] = info.ServerName
	}
	if !reflect.DeepEqual(got, map[string]string{"alpha": "server", "omega": "server", "server": "other"}) {
		t.Fatalf("infos: %v", got)
	}
}
