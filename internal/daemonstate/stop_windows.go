//go:build windows

package daemonstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	// These narrow OS seams make stale-PID behavior deterministic in tests. A
	// failed process open is never interpreted without a separate inventory.
	openDaemonProcess   = windows.OpenProcess
	daemonProcessExists = processExists
)

func stop(home string) error {
	state, err := Load(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	handle, err := openDaemonProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(state.PID))
	if err != nil {
		exists, inventoryErr := daemonProcessExists(uint32(state.PID))
		if inventoryErr != nil {
			return errors.Join(fmt.Errorf("open recorded LCTK daemon process %d: %w", state.PID, err), inventoryErr)
		}
		if !exists {
			_ = os.Remove(filepath.Join(home, FileName))
			return nil
		}
		return fmt.Errorf("open recorded LCTK daemon process %d: %w", state.PID, err)
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return err
	}
	actual := filepath.Clean(windows.UTF16ToString(buffer[:size]))
	if !strings.EqualFold(actual, state.Executable) {
		return errors.New("recorded daemon PID now belongs to another executable")
	}
	if err := windows.TerminateProcess(handle, 0); err != nil {
		return err
	}
	// Activation must not race the old daemon's final file handles or listener.
	// Wait on the verified process handle rather than polling an unrelated PID.
	wait, err := windows.WaitForSingleObject(handle, 10_000)
	if err != nil {
		return err
	}
	if wait != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("LCTK daemon did not stop within 10 seconds (wait result %d)", wait)
	}
	_ = os.Remove(filepath.Join(home, FileName))
	return nil
}

// processExists checks the Windows process inventory without requesting a
// handle to the target. This distinguishes a stale PID document from a live
// elevated or protected process that setup must never terminate by guesswork.
func processExists(pid uint32) (bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, fmt.Errorf("snapshot Windows processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return false, nil
		}
		return false, fmt.Errorf("read first Windows process: %w", err)
	}
	for {
		if entry.ProcessID == pid {
			return true, nil
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return false, nil
			}
			return false, fmt.Errorf("read Windows process inventory: %w", err)
		}
	}
}
