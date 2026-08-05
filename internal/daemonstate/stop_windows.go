//go:build windows

package daemonstate

import (
	"errors"
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
		return nil
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
	_ = os.Remove(filepath.Join(home, FileName))
	return nil
}
