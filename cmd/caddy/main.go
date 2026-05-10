// Standalone Caddy build that includes the starlark handler. Useful for
// local testing without xcaddy:
//
//	go run ./cmd/caddy run --config examples/Caddyfile --adapter caddyfile
package main

import (
	caddycmd "github.com/caddyserver/caddy/v2/cmd"

	// Standard Caddy modules.
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	// This module.
	_ "github.com/dbohdan/caddy-starlark"
)

func main() { caddycmd.Main() }
