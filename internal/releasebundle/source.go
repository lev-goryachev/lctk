package releasebundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

const maxEnvelopeBytes = 4 << 20

// DefaultManifestURL is embedded into official host binaries. Development
// builds leave it empty and must receive an explicit source.
var DefaultManifestURL = ""

// Load reads one bounded local or HTTPS envelope and authenticates it before a
// caller can use any component URL contained in the payload.
func Load(ctx context.Context, source string, client *http.Client, verifier Verifier) (Manifest, error) {
	if source == "" {
		source = DefaultManifestURL
	}
	if source == "" {
		return Manifest{}, errors.New("no signed release manifest source was supplied")
	}
	document, err := readSource(ctx, source, client)
	if err != nil {
		return Manifest{}, err
	}
	return verifier.Verify(document)
}

func readSource(ctx context.Context, source string, client *http.Client) ([]byte, error) {
	if filepath.IsAbs(source) {
		return readLocalSource(source)
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse release manifest source: %w", err)
	}
	switch parsed.Scheme {
	case "https":
		if client == nil {
			client = http.DefaultClient
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, fmt.Errorf("create release manifest request: %w", err)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("download release manifest: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download release manifest returned %s", response.Status)
		}
		return readBounded(response.Body)
	case "":
		return readLocalSource(source)
	default:
		return nil, errors.New("release manifest source must be an HTTPS URL or local file")
	}
}

func readLocalSource(source string) ([]byte, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return nil, fmt.Errorf("resolve release manifest path: %w", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("open release manifest: %w", err)
	}
	defer file.Close()
	return readBounded(file)
}

func readBounded(reader io.Reader) ([]byte, error) {
	document, err := io.ReadAll(io.LimitReader(reader, maxEnvelopeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read release manifest: %w", err)
	}
	if len(document) == 0 || len(document) > maxEnvelopeBytes {
		return nil, errors.New("release manifest is empty or exceeds the size limit")
	}
	return document, nil
}
