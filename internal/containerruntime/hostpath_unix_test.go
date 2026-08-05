//go:build !windows

package containerruntime

import "testing"

func TestHostPathKeepsAbsolutePath(t *testing.T) {
	got, err := HostPath("/work/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/work/alpha" {
		t.Fatalf("HostPath = %q", got)
	}
}
