//go:build windows

package daemonstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func stop(home string) error {
	state, err := Load(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(state.PID))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			_ = os.Remove(filepath.Join(home, FileName))
			return nil
		}
		return err
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
