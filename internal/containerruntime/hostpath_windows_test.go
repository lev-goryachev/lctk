//go:build windows

package containerruntime

import "testing"

func TestHostPathTranslatesLocalDrive(t *testing.T) {
	got, err := HostPath(`D:\Projects\alpha beta`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/mnt/d/Projects/alpha beta" {
		t.Fatalf("HostPath = %q", got)
	}
}

func TestHostPathRefusesUNC(t *testing.T) {
	if _, err := HostPath(`\\server\share\project`); err == nil {
		t.Fatal("HostPath accepted a UNC path without a sharing contract")
	}
}
