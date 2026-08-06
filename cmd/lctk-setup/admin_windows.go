//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/lev-goryachev/lctk/internal/adminclient"
	"github.com/lev-goryachev/lctk/internal/adminsession"
	"github.com/lev-goryachev/lctk/internal/lctkhome"
	"github.com/lev-goryachev/lctk/internal/windowsprocess"
	"golang.org/x/sys/windows"
)

const (
	adminWindowWidth  = 1120
	adminWindowHeight = 790

	idAdminProjectPath = 2001
	idAdminBrowse      = 2002
	idAdminProfile     = 2003
	idAdminAdd         = 2004
	idAdminProjects    = 2005
	idAdminStart       = 2006
	idAdminStop        = 2007
	idAdminRestart     = 2008
	idAdminReindex     = 2009
	idAdminCodex       = 2010
	idAdminMode        = 2011
	idAdminApplyMode   = 2012
	idAdminGrants      = 2013
	idAdminRevoke      = 2014
	idAdminRefresh     = 2015
	idAdminUninstall   = 2016

	wsVScroll       = 0x00200000
	esMultiline     = 0x0004
	esAutoVScroll   = 0x0040
	esReadOnly      = 0x0800
	lbsNotify       = 0x0001
	cbsDropDownList = 0x0003
	wmAppAdminState = 0x8002
	lbnSelChange    = 1
	lbResetContent  = 0x0184
	lbAddString     = 0x0180
	lbGetCurSel     = 0x0188
	lbSetCurSel     = 0x0186
	cbAddString     = 0x0143
	cbGetCurSel     = 0x0147
	cbSetCurSel     = 0x014E
)

var (
	adminWindowClass = mustUTF16("LCTKNativeAdminWindow")
	adminWindows     sync.Map
)

// nativeAdminWindow renders the daemon-owned administrator state with Win32
// controls. It deliberately contains no project authority or filesystem logic;
// every mutation goes through the authenticated loopback API.
type nativeAdminWindow struct {
	client  *adminclient.Client
	window  uintptr
	context context.Context
	cancel  context.CancelFunc

	statusLabel uintptr
	projectPath uintptr
	profile     uintptr
	projectList uintptr
	projectInfo uintptr
	mode        uintptr
	grantList   uintptr
	logs        uintptr
	controls    []uintptr
	buttons     map[uint16]uintptr

	mu       sync.RWMutex
	snapshot adminclient.Snapshot
	status   string
	failure  string
	busy     bool
}

// runAdminWindow ensures the background daemon is reachable, spends its
// current exchange code directly from this process, and enters the native
// message loop. Nothing is opened in or delegated to a browser.
func runAdminWindow(parent context.Context, address string) error {
	if err := ensureAdminDaemon(parent, address); err != nil {
		return err
	}
	code, err := adminsession.ReadCode("")
	if err != nil {
		return err
	}
	connectCtx, cancel := context.WithTimeout(parent, 10*time.Second)
	client, err := adminclient.Connect(connectCtx, address, code)
	cancel()
	if err != nil {
		return err
	}
	window, err := newNativeAdminWindow(parent, client)
	if err != nil {
		return err
	}
	defer window.cancel()
	window.refresh("Loading LCTK status...")
	procShowWindow.Call(window.window, swShow)
	procUpdateWindow.Call(window.window)

	var current message
	for {
		result, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&current)), 0, 0, 0)
		if int32(result) == -1 {
			return fmt.Errorf("read admin window message: %w", callErr)
		}
		if result == 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&current)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&current)))
	}
}

func ensureAdminDaemon(parent context.Context, address string) error {
	if adminDaemonReady(parent, address) {
		return nil
	}
	home, err := lctkhome.Dir()
	if err != nil {
		return err
	}
	launcher := filepath.Join(home, "bin", "lctk.exe")
	command := exec.Command(launcher, "daemon", "--listen", address)
	windowsprocess.HideConsole(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start LCTK daemon: %w", err)
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("detach LCTK daemon: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if adminDaemonReady(parent, address) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("LCTK daemon did not become ready within 15 seconds")
}

func adminDaemonReady(parent context.Context, address string) bool {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/health", nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func newNativeAdminWindow(parent context.Context, client *adminclient.Client) (*nativeAdminWindow, error) {
	instance, _, _ := procGetModuleHandleW.Call(0)
	cursor, _, _ := procLoadCursorW.Call(0, idArrow)
	class := windowClassEx{
		Size: uint32(unsafe.Sizeof(windowClassEx{})), WindowProc: windows.NewCallback(adminWindowProc),
		Instance: instance, Cursor: cursor, Background: colorWindow + 1, ClassName: adminWindowClass,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	screenWidth, _, _ := procGetSystemMetrics.Call(0)
	screenHeight, _, _ := procGetSystemMetrics.Call(1)
	x := (int32(screenWidth) - adminWindowWidth) / 2
	y := (int32(screenHeight) - adminWindowHeight) / 2
	handle, _, callErr := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(adminWindowClass)), uintptr(unsafe.Pointer(mustUTF16("LCTK"))),
		wsOverlappedWindow, uintptr(x), uintptr(y), adminWindowWidth, adminWindowHeight,
		0, 0, instance, 0,
	)
	if handle == 0 {
		return nil, fmt.Errorf("create native admin window: %w", callErr)
	}
	ctx, cancel := context.WithCancel(parent)
	window := &nativeAdminWindow{client: client, window: handle, context: ctx, cancel: cancel, status: "Connecting...", buttons: map[uint16]uintptr{}}
	adminWindows.Store(handle, window)
	if err := window.createControls(instance); err != nil {
		adminWindows.Delete(handle)
		procDestroyWindow.Call(handle)
		cancel()
		return nil, err
	}
	window.render()
	return window, nil
}

func (window *nativeAdminWindow) createControls(instance uintptr) error {
	font, _, _ := procGetStockObject.Call(defaultGUIFont)
	create := func(className, text string, style uint32, x, y, width, height int32, id uintptr) (uintptr, error) {
		handle, _, callErr := procCreateWindowExW.Call(
			0, uintptr(unsafe.Pointer(mustUTF16(className))), uintptr(unsafe.Pointer(mustUTF16(text))), uintptr(style),
			uintptr(x), uintptr(y), uintptr(width), uintptr(height), window.window, id, instance, 0,
		)
		if handle == 0 {
			return 0, fmt.Errorf("create %s admin control: %w", className, callErr)
		}
		procSendMessageW.Call(handle, wmSetFont, font, 1)
		return handle, nil
	}
	label := func(text string, x, y, width, height int32) error {
		_, err := create("STATIC", text, wsChild|wsVisible|ssLeft, x, y, width, height, 0)
		return err
	}
	if err := label("LCTK", 24, 16, 160, 28); err != nil {
		return err
	}
	var err error
	window.statusLabel, err = create("STATIC", "", wsChild|wsVisible|ssLeft, 100, 18, 980, 46, 0)
	if err != nil {
		return err
	}
	if err := label("Add project", 24, 67, 160, 22); err != nil {
		return err
	}
	window.projectPath, err = create("EDIT", "", wsChild|wsVisible|wsTabStop|wsBorder|esAutoHScroll, 24, 92, 670, 28, idAdminProjectPath)
	if err != nil {
		return err
	}
	browse, err := create("BUTTON", "Browse...", wsChild|wsVisible|wsTabStop|bsPushButton, 704, 91, 88, 30, idAdminBrowse)
	if err != nil {
		return err
	}
	window.controls = append(window.controls, browse)
	window.profile, err = create("COMBOBOX", "", wsChild|wsVisible|wsTabStop|wsVScroll|cbsDropDownList, 802, 91, 100, 180, idAdminProfile)
	if err != nil {
		return err
	}
	addComboItems(window.profile, []string{"minimal", "full"}, 0)
	add, err := create("BUTTON", "Add", wsChild|wsVisible|wsTabStop|bsPushButton, 912, 91, 170, 30, idAdminAdd)
	if err != nil {
		return err
	}
	window.controls = append(window.controls, add)

	if err := label("Projects", 24, 136, 160, 22); err != nil {
		return err
	}
	window.projectList, err = create("LISTBOX", "", wsChild|wsVisible|wsTabStop|wsBorder|wsVScroll|lbsNotify, 24, 160, 430, 214, idAdminProjects)
	if err != nil {
		return err
	}
	window.projectInfo, err = create("STATIC", "Select a project.", wsChild|wsVisible|ssLeft, 470, 160, 612, 88, 0)
	if err != nil {
		return err
	}
	buttonSpecs := []struct {
		text        string
		id          uintptr
		x, y, width int32
	}{
		{"Start", idAdminStart, 470, 260, 82}, {"Stop", idAdminStop, 560, 260, 82},
		{"Restart", idAdminRestart, 650, 260, 82}, {"Reindex", idAdminReindex, 740, 260, 82},
		{"Configure & Open Codex", idAdminCodex, 830, 260, 252},
	}
	for _, spec := range buttonSpecs {
		button, createErr := create("BUTTON", spec.text, wsChild|wsVisible|wsTabStop|bsPushButton, spec.x, spec.y, spec.width, 31, spec.id)
		if createErr != nil {
			return createErr
		}
		window.controls = append(window.controls, button)
		window.buttons[uint16(spec.id)] = button
	}
	if err := label("Resource mode", 470, 311, 110, 22); err != nil {
		return err
	}
	window.mode, err = create("COMBOBOX", "", wsChild|wsVisible|wsTabStop|wsVScroll|cbsDropDownList, 585, 306, 120, 180, idAdminMode)
	if err != nil {
		return err
	}
	addComboItems(window.mode, []string{"quiet", "normal", "fast"}, 1)
	applyMode, err := create("BUTTON", "Apply mode", wsChild|wsVisible|wsTabStop|bsPushButton, 716, 305, 130, 31, idAdminApplyMode)
	if err != nil {
		return err
	}
	window.controls = append(window.controls, applyMode)
	window.buttons[idAdminApplyMode] = applyMode

	if err := label("Client grants", 24, 392, 160, 22); err != nil {
		return err
	}
	window.grantList, err = create("LISTBOX", "", wsChild|wsVisible|wsTabStop|wsBorder|wsVScroll|lbsNotify, 24, 416, 822, 116, idAdminGrants)
	if err != nil {
		return err
	}
	revoke, err := create("BUTTON", "Revoke selected", wsChild|wsVisible|wsTabStop|bsPushButton, 860, 416, 222, 32, idAdminRevoke)
	if err != nil {
		return err
	}
	window.controls = append(window.controls, revoke)
	window.buttons[idAdminRevoke] = revoke

	if err := label("Daemon log", 24, 550, 160, 22); err != nil {
		return err
	}
	window.logs, err = create("EDIT", "", wsChild|wsVisible|wsBorder|wsVScroll|esMultiline|esAutoVScroll|esReadOnly, 24, 574, 822, 135, 0)
	if err != nil {
		return err
	}
	refresh, err := create("BUTTON", "Refresh", wsChild|wsVisible|wsTabStop|bsPushButton, 860, 574, 222, 34, idAdminRefresh)
	if err != nil {
		return err
	}
	uninstallButton, err := create("BUTTON", "Uninstall LCTK", wsChild|wsVisible|wsTabStop|bsPushButton, 860, 675, 222, 34, idAdminUninstall)
	if err != nil {
		return err
	}
	window.controls = append(window.controls, refresh, uninstallButton)
	return nil
}

func adminWindowProc(windowHandle uintptr, message uint32, wParam, lParam uintptr) uintptr {
	value, exists := adminWindows.Load(windowHandle)
	if !exists {
		result, _, _ := procDefWindowProcW.Call(windowHandle, uintptr(message), wParam, lParam)
		return result
	}
	window := value.(*nativeAdminWindow)
	switch message {
	case wmCommand:
		id := uint16(wParam & 0xffff)
		notification := uint16((wParam >> 16) & 0xffff)
		if id == idAdminProjects && notification == lbnSelChange {
			window.renderSelection()
			return 0
		}
		if id == idAdminGrants && notification == lbnSelChange {
			window.renderSelection()
			return 0
		}
		window.handleCommand(id)
		return 0
	case wmAppAdminState:
		window.render()
		return 0
	case wmClose:
		window.cancel()
		procDestroyWindow.Call(window.window)
		return 0
	case wmDestroy:
		adminWindows.Delete(windowHandle)
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(windowHandle, uintptr(message), wParam, lParam)
	return result
}

func (window *nativeAdminWindow) handleCommand(id uint16) {
	switch id {
	case idAdminBrowse:
		if selected := browseForFolder(window.window, "Choose a project directory"); selected != "" {
			setWindowText(window.projectPath, selected)
		}
	case idAdminAdd:
		path := strings.TrimSpace(windowText(window.projectPath))
		profile := selectedComboText(window.profile, []string{"minimal", "full"})
		if path == "" {
			window.fail("Choose a project directory first.")
			return
		}
		window.act("Registering project...", func(ctx context.Context) error { return window.client.AddProject(ctx, path, profile) })
	case idAdminStart:
		window.projectAction("start", "Starting project...")
	case idAdminStop:
		window.projectAction("stop", "Stopping project...")
	case idAdminRestart:
		window.projectAction("restart", "Restarting project...")
	case idAdminReindex:
		window.projectAction("reindex", "Reindexing project...")
	case idAdminCodex:
		window.projectAction("codex", "Configuring and opening Codex...")
	case idAdminApplyMode:
		project, ok := window.selectedProject()
		if !ok {
			window.fail("Select a project first.")
			return
		}
		mode := selectedComboText(window.mode, []string{"quiet", "normal", "fast"})
		window.act("Changing resource mode...", func(ctx context.Context) error { return window.client.SetProjectMode(ctx, project.ID, mode) })
	case idAdminRevoke:
		grant, ok := window.selectedGrant()
		if !ok {
			window.fail("Select a client grant first.")
			return
		}
		window.act("Revoking client grant...", func(ctx context.Context) error { return window.client.RevokeGrant(ctx, grant.ID) })
	case idAdminRefresh:
		window.refresh("Refreshing LCTK status...")
	case idAdminUninstall:
		answer, _ := windows.MessageBox(windows.HWND(window.window), mustUTF16("Open the complete LCTK uninstall dialog?"), mustUTF16("Uninstall LCTK"), windows.MB_YESNO|windows.MB_ICONWARNING)
		if answer != messageBoxYes {
			return
		}
		window.launchUninstaller()
	}
}

func (window *nativeAdminWindow) projectAction(action, status string) {
	project, ok := window.selectedProject()
	if !ok {
		window.fail("Select a project first.")
		return
	}
	window.act(status, func(ctx context.Context) error { return window.client.ProjectAction(ctx, project.ID, action) })
}

func (window *nativeAdminWindow) selectedProject() (adminclient.Project, bool) {
	index, _, _ := procSendMessageW.Call(window.projectList, lbGetCurSel, 0, 0)
	window.mu.RLock()
	defer window.mu.RUnlock()
	if int32(index) < 0 || int(index) >= len(window.snapshot.Projects) {
		return adminclient.Project{}, false
	}
	return window.snapshot.Projects[index], true
}

func (window *nativeAdminWindow) selectedGrant() (adminclient.Grant, bool) {
	index, _, _ := procSendMessageW.Call(window.grantList, lbGetCurSel, 0, 0)
	window.mu.RLock()
	defer window.mu.RUnlock()
	if int32(index) < 0 || int(index) >= len(window.snapshot.Grants) {
		return adminclient.Grant{}, false
	}
	return window.snapshot.Grants[index], true
}

func (window *nativeAdminWindow) act(status string, operation func(context.Context) error) {
	window.mu.Lock()
	if window.busy {
		window.mu.Unlock()
		return
	}
	window.busy, window.status, window.failure = true, status, ""
	window.mu.Unlock()
	window.postState()
	go func() {
		ctx, cancel := context.WithTimeout(window.context, 4*time.Minute)
		err := operation(ctx)
		cancel()
		if err == nil {
			ctx, cancel = context.WithTimeout(window.context, 3*time.Minute)
			snapshot, loadErr := window.client.Load(ctx)
			cancel()
			if loadErr == nil {
				window.mu.Lock()
				window.snapshot = snapshot
				window.mu.Unlock()
			}
			err = loadErr
		}
		window.mu.Lock()
		window.busy = false
		if err != nil {
			window.failure, window.status = err.Error(), "The action failed."
		} else {
			window.failure, window.status = "", "Ready."
		}
		window.mu.Unlock()
		window.postState()
	}()
}

func (window *nativeAdminWindow) refresh(status string) {
	window.mu.Lock()
	if window.busy {
		window.mu.Unlock()
		return
	}
	window.busy, window.status, window.failure = true, status, ""
	window.mu.Unlock()
	window.postState()
	go func() {
		ctx, cancel := context.WithTimeout(window.context, 3*time.Minute)
		snapshot, err := window.client.Load(ctx)
		cancel()
		window.mu.Lock()
		window.busy = false
		if err != nil {
			window.failure, window.status = err.Error(), "Could not load LCTK status."
		} else {
			window.snapshot, window.failure, window.status = snapshot, "", "Ready."
		}
		window.mu.Unlock()
		window.postState()
	}()
}

func (window *nativeAdminWindow) launchUninstaller() {
	window.mu.Lock()
	if window.busy {
		window.mu.Unlock()
		return
	}
	window.busy, window.status, window.failure = true, "Opening the uninstaller...", ""
	window.mu.Unlock()
	window.postState()
	go func() {
		ctx, cancel := context.WithTimeout(window.context, 10*time.Second)
		err := window.client.LaunchUninstaller(ctx)
		cancel()
		if err != nil {
			window.mu.Lock()
			window.busy, window.failure, window.status = false, err.Error(), "Could not open the uninstaller."
			window.mu.Unlock()
			window.postState()
			return
		}
		procPostMessageW.Call(window.window, wmClose, 0, 0)
	}()
}

func (window *nativeAdminWindow) fail(message string) {
	window.mu.Lock()
	window.failure, window.status = message, "Action required."
	window.mu.Unlock()
	window.postState()
}

func (window *nativeAdminWindow) postState() {
	procPostMessageW.Call(window.window, wmAppAdminState, 0, 0)
}

func (window *nativeAdminWindow) render() {
	window.mu.RLock()
	snapshot, status, failure, busy := window.snapshot, window.status, window.failure, window.busy
	window.mu.RUnlock()
	runtimeText := "runtime unavailable"
	if snapshot.Overview.Runtime.Available {
		runtimeText = strings.TrimSpace(snapshot.Overview.Runtime.Provider + " " + snapshot.Overview.Runtime.Version + " " + snapshot.Overview.Runtime.OSType)
	} else if snapshot.Overview.Runtime.Detail != "" {
		runtimeText += ": " + snapshot.Overview.Runtime.Detail
	}
	header := status
	if snapshot.Overview.Version != "" {
		header = fmt.Sprintf("LCTK %s | %s | %s", snapshot.Overview.Version, runtimeText, status)
	}
	if failure != "" {
		header += "  Error: " + failure
	}
	setWindowText(window.statusLabel, header)
	selected, _, _ := procSendMessageW.Call(window.projectList, lbGetCurSel, 0, 0)
	procSendMessageW.Call(window.projectList, lbResetContent, 0, 0)
	for _, project := range snapshot.Projects {
		text := fmt.Sprintf("%s  |  %s  |  %s  |  %s", project.Name, project.State, project.Mode, project.Path)
		procSendMessageW.Call(window.projectList, lbAddString, 0, uintptr(unsafe.Pointer(mustUTF16(text))))
	}
	if int32(selected) >= 0 && int(selected) < len(snapshot.Projects) {
		procSendMessageW.Call(window.projectList, lbSetCurSel, selected, 0)
	}
	procSendMessageW.Call(window.grantList, lbResetContent, 0, 0)
	for _, grant := range snapshot.Grants {
		state := "active"
		if grant.Revoked {
			state = "revoked"
		}
		text := fmt.Sprintf("%s  |  %s  |  %s  |  %s", grant.Client, strings.Join(grant.Projects, ", "), grant.IssuedAt, state)
		procSendMessageW.Call(window.grantList, lbAddString, 0, uintptr(unsafe.Pointer(mustUTF16(text))))
	}
	setWindowText(window.logs, renderAdminLogs(snapshot))
	setEnabled(window.projectPath, !busy)
	setEnabled(window.profile, !busy)
	for _, control := range window.controls {
		setEnabled(control, !busy)
	}
	window.renderSelection()
}

func (window *nativeAdminWindow) renderSelection() {
	window.mu.RLock()
	busy := window.busy
	window.mu.RUnlock()
	project, ok := window.selectedProject()
	if !ok {
		setWindowText(window.projectInfo, "No project selected.")
	} else {
		index := "not available"
		if project.Index != nil {
			index = fmt.Sprintf("%d files, generation %d", project.Index.FileCount, project.Index.Generation)
			if project.Index.Indexing {
				index += " (indexing)"
			}
		}
		changes := "not watched"
		if project.Changes != nil {
			changes = fmt.Sprintf("%d pending, %d directories", project.Changes.Pending, project.Changes.Directories)
			if !project.Changes.Complete {
				changes += ", incomplete: " + project.Changes.GapReason
			}
		}
		detail := fmt.Sprintf("%s\r\n%s\r\nState: %s; health: %s; index: %s; changes: %s; disk: %s", project.Name, project.Path, project.State, project.Health, index, changes, project.Disk.Human)
		if project.Detail != "" {
			detail += "\r\n" + project.Detail
		}
		setWindowText(window.projectInfo, detail)
		modes := []string{"quiet", "normal", "fast"}
		for index, mode := range modes {
			if mode == project.Mode {
				procSendMessageW.Call(window.mode, cbSetCurSel, uintptr(index), 0)
				break
			}
		}
	}
	running := ok && project.State == "running"
	setEnabled(window.buttons[idAdminStart], !busy && ok && !running)
	for _, id := range []uint16{idAdminStop, idAdminRestart, idAdminReindex} {
		setEnabled(window.buttons[id], !busy && running)
	}
	setEnabled(window.buttons[idAdminCodex], !busy && ok)
	setEnabled(window.buttons[idAdminApplyMode], !busy && ok)
	setEnabled(window.mode, !busy && ok)
	_, grantSelected := window.selectedGrant()
	setEnabled(window.buttons[idAdminRevoke], !busy && grantSelected)
}

func addComboItems(handle uintptr, values []string, selected int) {
	for _, value := range values {
		procSendMessageW.Call(handle, cbAddString, 0, uintptr(unsafe.Pointer(mustUTF16(value))))
	}
	procSendMessageW.Call(handle, cbSetCurSel, uintptr(selected), 0)
}

func selectedComboText(handle uintptr, values []string) string {
	index, _, _ := procSendMessageW.Call(handle, cbGetCurSel, 0, 0)
	if int32(index) < 0 || int(index) >= len(values) {
		return ""
	}
	return values[index]
}

func renderAdminLogs(snapshot adminclient.Snapshot) string {
	lines := make([]string, 0, len(snapshot.Logs))
	start := 0
	if len(snapshot.Logs) > 200 {
		start = len(snapshot.Logs) - 200
	}
	for _, record := range snapshot.Logs[start:] {
		keys := make([]string, 0, len(record.Fields))
		for key := range record.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		line := fmt.Sprintf("%s  %s  %s", record.At.Format(time.RFC3339), record.Level, record.Message)
		for _, key := range keys {
			line += "  " + key + "=" + record.Fields[key]
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "Nothing logged yet."
	}
	return strings.Join(lines, "\r\n")
}
