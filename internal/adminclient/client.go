// Package adminclient is the native desktop client's authenticated transport
// to the loopback administrator API. The daemon remains the only authority for
// project lifecycle, grants, and diagnostics; the Windows window only renders
// that authority and sends explicit operator actions.
package adminclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/lev-goryachev/lctk/internal/adminsession"
	"github.com/lev-goryachev/lctk/internal/hostsettings"
	"github.com/lev-goryachev/lctk/internal/logring"
)

// Client owns one short-lived native administrator session. Both authorization
// headers remain in process memory and are never persisted.
type Client struct {
	base  *url.URL
	http  *http.Client
	csrf  string
	token string
}

// Overview is the machine summary rendered at the top of the native window.
type Overview struct {
	Version      string                `json:"version"`
	Runtime      Runtime               `json:"runtime"`
	Home         string                `json:"home"`
	Settings     hostsettings.Settings `json:"settings"`
	ServerTime   string                `json:"server_time"`
	ProjectCount int                   `json:"project_count"`
}

// Runtime reports the managed container provider's current availability.
type Runtime struct {
	Available bool   `json:"available"`
	Provider  string `json:"provider,omitempty"`
	Version   string `json:"version,omitempty"`
	OSType    string `json:"os_type,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Project is one administrator-visible project and its current derived state.
type Project struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Profile string  `json:"profile"`
	Path    string  `json:"path"`
	State   string  `json:"state"`
	Health  string  `json:"health,omitempty"`
	Detail  string  `json:"detail,omitempty"`
	Mode    string  `json:"mode"`
	Index   *Index  `json:"index,omitempty"`
	Changes *Change `json:"changes,omitempty"`
	Disk    Disk    `json:"disk"`
}

// Index is the latest code-intelligence status returned for a running project.
type Index struct {
	Ready      bool   `json:"ready"`
	Indexing   bool   `json:"indexing"`
	Generation uint64 `json:"generation"`
	FileCount  int    `json:"file_count"`
	IndexedAt  string `json:"indexed_at,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Change is the native watcher and reconciliation status for one project.
type Change struct {
	Watching    bool   `json:"watching"`
	Pending     int    `json:"pending"`
	Complete    bool   `json:"complete"`
	GapReason   string `json:"gap_reason,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	Directories int    `json:"directories"`
}

// Disk contains the current project index storage measurement.
type Disk struct {
	SourceBytes int64  `json:"source_bytes"`
	IndexBytes  int64  `json:"index_bytes"`
	Human       string `json:"human"`
}

// Grant is a revocable client authorization. The credential itself is never
// returned by the API and therefore cannot appear in the desktop process.
type Grant struct {
	ID       string   `json:"id"`
	Client   string   `json:"client"`
	Projects []string `json:"projects"`
	IssuedAt string   `json:"issued_at,omitempty"`
	Expires  string   `json:"expires,omitempty"`
	Revoked  bool     `json:"revoked,omitempty"`
}

// Snapshot is one internally consistent refresh result for the native window.
type Snapshot struct {
	Overview Overview
	Projects []Project
	Grants   []Grant
	Logs     []logring.Record
}

// Connect exchanges the daemon's current one-time code for an in-memory native
// session. No browser, URL credential, or persistent cookie is involved.
func Connect(ctx context.Context, address, code string) (*Client, error) {
	base, err := url.Parse("http://" + strings.TrimSpace(address))
	if err != nil || base.Host == "" || base.Path != "" || base.User != nil || !adminsession.LoopbackHost(base.Host) {
		return nil, errors.New("admin API address is invalid")
	}
	client := &Client{base: base, http: &http.Client{}}
	var response struct {
		Session string `json:"session"`
		CSRF    string `json:"csrf"`
	}
	if err := client.do(ctx, http.MethodPost, "/admin/session", map[string]string{"code": strings.TrimSpace(code)}, &response, false); err != nil {
		return nil, fmt.Errorf("open native admin session: %w", err)
	}
	if response.CSRF == "" {
		return nil, errors.New("daemon returned an empty admin authorization token")
	}
	client.csrf, client.token = response.CSRF, response.Session
	if client.token == "" {
		return nil, errors.New("daemon returned no admin session credential")
	}
	return client, nil
}

// Load retrieves every section shown in the native administrator window.
func (client *Client) Load(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	if err := client.do(ctx, http.MethodGet, "/admin/api/overview", nil, &snapshot.Overview, true); err != nil {
		return Snapshot{}, err
	}
	var projects struct {
		Projects []Project `json:"projects"`
	}
	if err := client.do(ctx, http.MethodGet, "/admin/api/projects", nil, &projects, true); err != nil {
		return Snapshot{}, err
	}
	var grants struct {
		Grants []Grant `json:"grants"`
	}
	if err := client.do(ctx, http.MethodGet, "/admin/api/grants", nil, &grants, true); err != nil {
		return Snapshot{}, err
	}
	var logs struct {
		Records []logring.Record `json:"records"`
	}
	if err := client.do(ctx, http.MethodGet, "/admin/api/logs", nil, &logs, true); err != nil {
		return Snapshot{}, err
	}
	snapshot.Projects, snapshot.Grants, snapshot.Logs = projects.Projects, grants.Grants, logs.Records
	return snapshot, nil
}

// AddProject registers one absolute project directory with an explicit profile.
func (client *Client) AddProject(ctx context.Context, path, profile string) error {
	return client.do(ctx, http.MethodPost, "/admin/api/projects", map[string]string{
		"path": strings.TrimSpace(path), "profile": strings.TrimSpace(profile),
	}, nil, true)
}

// ProjectAction performs a lifecycle, indexing, or Codex action against the
// exact server-issued project identifier selected in the native list.
func (client *Client) ProjectAction(ctx context.Context, projectID, action string) error {
	path := "/admin/api/projects/" + url.PathEscape(projectID) + "/" + url.PathEscape(action)
	return client.do(ctx, http.MethodPost, path, map[string]string{}, nil, true)
}

// SetProjectMode changes the project's background resource policy.
func (client *Client) SetProjectMode(ctx context.Context, projectID, mode string) error {
	path := "/admin/api/projects/" + url.PathEscape(projectID) + "/mode"
	return client.do(ctx, http.MethodPost, path, map[string]string{"mode": mode}, nil, true)
}

// RevokeGrant invalidates one exact server-issued client grant.
func (client *Client) RevokeGrant(ctx context.Context, grantID string) error {
	return client.do(ctx, http.MethodDelete, "/admin/api/grants/"+url.PathEscape(grantID), nil, nil, true)
}

// LaunchUninstaller asks the daemon to start the installed GUI uninstaller in
// a separate process, allowing this window to close before files are removed.
func (client *Client) LaunchUninstaller(ctx context.Context) error {
	return client.do(ctx, http.MethodPost, "/admin/api/uninstall", map[string]string{}, nil, true)
}

func (client *Client) do(ctx context.Context, method, path string, input, output any, authenticated bool) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *client.base
	endpoint.Path = path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated && method != http.MethodGet {
		request.Header.Set(adminsession.HeaderCSRF, client.csrf)
	}
	if authenticated {
		request.Header.Set(adminsession.HeaderSession, client.token)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("admin API %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 4<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(limited).Decode(&failure)
		if failure.Error == "" {
			failure.Error = response.Status
		}
		return errors.New(failure.Error)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, limited)
		return err
	}
	if err := json.NewDecoder(limited).Decode(output); err != nil {
		return fmt.Errorf("decode admin API %s: %w", path, err)
	}
	return nil
}
