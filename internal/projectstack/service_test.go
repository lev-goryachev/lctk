package projectstack

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// TestInspectReadsThePublishedServiceAddress covers the delimited inspect
// output. Health and the published port are both allowed to be empty, so the
// fields are separated explicitly; parsing them positionally by whitespace would
// silently read a port as a health status.
func TestInspectReadsThePublishedServiceAddress(t *testing.T) {
	cases := []struct {
		name        string
		stdout      string
		wantState   State
		wantHealth  string
		wantAddress string
	}{
		{
			name:        "running with a published port",
			stdout:      "running|healthy|49155\n",
			wantState:   StateRunning,
			wantHealth:  "healthy",
			wantAddress: "127.0.0.1:49155",
		},
		{
			name:       "running with no published port",
			stdout:     "running|healthy|\n",
			wantState:  StateRunning,
			wantHealth: "healthy",
		},
		{
			name:        "no healthcheck but a published port",
			stdout:      "running||49155\n",
			wantState:   StateStarting,
			wantAddress: "127.0.0.1:49155",
		},
		{
			name:      "exited container publishes nothing",
			stdout:    "exited||\n",
			wantState: StateStopped,
		},
		{
			// A container created by an earlier build reports the older
			// whitespace-separated form. It must still be readable rather than
			// unparseable, because the fix is a restart the operator has not made
			// yet.
			name:       "older whitespace form still parses",
			stdout:     "running healthy\n",
			wantState:  StateRunning,
			wantHealth: "healthy",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{responses: []fakeResponse{
				{match: "version", stdout: "29.5.3 linux\n"},
				{match: "inspect", stdout: testCase.stdout},
			}}
			manager := NewManagerWithRunner(runner)

			status, err := manager.Status(context.Background(), testProject("alpha-abcd1234", absPath("work", "alpha")))
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.State != testCase.wantState {
				t.Errorf("state = %q, want %q", status.State, testCase.wantState)
			}
			if status.Health != testCase.wantHealth {
				t.Errorf("health = %q, want %q", status.Health, testCase.wantHealth)
			}
			if status.ServiceAddress != testCase.wantAddress {
				t.Errorf("service address = %q, want %q", status.ServiceAddress, testCase.wantAddress)
			}
		})
	}
}

// TestComposePublishesTheServiceOnLoopbackWithoutAFixedPort pins two properties
// that together let many projects run at once without coordination: the runtime
// chooses the host port, and it is never exposed beyond loopback.
func TestComposePublishesTheServiceOnLoopbackWithoutAFixedPort(t *testing.T) {
	rendered, err := Render(testProject("alpha-abcd1234", absPath("work", "alpha")))
	if err != nil {
		t.Fatal(err)
	}
	document := string(rendered)

	want := "127.0.0.1::" + strconv.Itoa(ServicePort)
	if !strings.Contains(document, want) {
		t.Errorf("compose does not publish %q:\n%s", want, document)
	}
	// A fixed host port would make two projects contend for one number, which is
	// exactly what per-project isolation must not require the operator to manage.
	fixed := "127.0.0.1:" + strconv.Itoa(ServicePort) + ":" + strconv.Itoa(ServicePort)
	if strings.Contains(document, fixed) {
		t.Errorf("compose pins a fixed host port:\n%s", document)
	}
	if strings.Contains(document, "0.0.0.0") {
		t.Errorf("compose exposes the service beyond loopback:\n%s", document)
	}
}

func TestComposeRenderingStaysReproducibleWithThePublishedPort(t *testing.T) {
	project := testProject("alpha-abcd1234", absPath("work", "alpha"))
	first, err := Render(project)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(project)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("rendering the same project twice produced different bytes")
	}
}
