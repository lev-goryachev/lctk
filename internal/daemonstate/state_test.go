package daemonstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterRecordsOnlyTheCurrentExecutableInsideHomeContract(t *testing.T) {
	home := t.TempDir()
	cleanup, err := Register(home)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	body, err := os.ReadFile(filepath.Join(home, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if state.PID != os.Getpid() || state.Executable == "" {
		t.Fatalf("state=%+v", state)
	}
	cleanup()
	if _, err := os.Stat(filepath.Join(home, FileName)); !os.IsNotExist(err) {
		t.Fatalf("state survived cleanup: %v", err)
	}
}

func TestLoadRejectsAnExecutableOutsideTheInstallation(t *testing.T) {
	home := t.TempDir()
	body := []byte(`{"pid":123,"executable":"C:\\Windows\\System32\\notepad.exe"}`)
	if err := os.WriteFile(filepath.Join(home, FileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("outside executable was trusted")
	}
}
