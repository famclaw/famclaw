// Package mdns advertises FamClaw on the local network via mDNS/Bonjour,
// making it accessible as famclaw.local from any device on the LAN.
package mdns

import (
	"log"

	"github.com/grandcat/zeroconf"
)

// Advertise registers the service with mDNS so it appears as <name>.local.
// This is a blocking call — run it in a goroutine.
func Advertise(name string, port int) {
	server, err := zeroconf.Register(
		"FamClaw",           // service instance name
		"_http._tcp",        // service type
		"local.",            // domain
		port,                // port
		[]string{            // TXT records
			"version=1.0",
			"app=famclaw",
		},
		nil, // use all interfaces
	)
	if err != nil {
		log.Printf("[mdns] Failed to register (mDNS will be unavailable): %v", err)
		return
	}
	defer server.Shutdown()

	log.Printf("[mdns] Advertising as %s.local:%d", name, port)
	// Block forever — the caller's goroutine keeps this alive
	select {}
}
