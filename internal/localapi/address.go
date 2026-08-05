// Package localapi defines the stable loopback control-plane identity shared by
// the daemon, desktop launcher, and generated client configuration.
package localapi

// DefaultAddress is the installation-wide loopback listener shared by the
// daemon, desktop launcher, and generated MCP client configuration.
const DefaultAddress = "127.0.0.1:4444"
