package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lev-goryachev/lctk/images/code-intel/internal/searchindex"
	"github.com/lev-goryachev/lctk/images/code-intel/internal/symbols"
)

// readableIndexer hands back one file's content, standing in for a store without
// building an index.
type readableIndexer struct {
	stubIndexer
	content []byte
	digest  string
	err     error
	// asked records the path and limit the route passed down, so a test can check
	// that the size limit reaching the store is the outliner's rather than a
	// constant repeated in the handler.
	asked      string
	askedLimit int64
}

func (r *readableIndexer) ReadProjectFile(relative string, maxBytes int64) ([]byte, string, error) {
	r.asked, r.askedLimit = relative, maxBytes
	if r.err != nil {
		return nil, "", r.err
	}
	return r.content, r.digest, nil
}

func newOutlineServer(t *testing.T, indexer Indexer, outliner Outliner) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(New(indexer, outliner, nil).Handler())
	t.Cleanup(server.Close)
	return server
}

func postOutline(t *testing.T, server *httptest.Server, body string) (*http.Response, []byte) {
	t.Helper()
	response, err := http.Post(server.URL+"/outline", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /outline: %v", err)
	}
	defer response.Body.Close()
	payload := make([]byte, 1<<16)
	read, _ := response.Body.Read(payload)
	return response, payload[:read]
}

func newOutliner(t *testing.T) *symbols.Engine {
	t.Helper()
	engine, err := symbols.New()
	if err != nil {
		t.Fatalf("build the symbol engine: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

func TestTheOutlineRouteReadsThroughTheStoreAndAnswersWithDeclarations(t *testing.T) {
	indexer := &readableIndexer{content: []byte("package p\n\nfunc Needle() {}\n"), digest: "abc"}
	outliner := newOutliner(t)
	server := newOutlineServer(t, indexer, outliner)

	response, payload := postOutline(t, server, `{"path":"internal/a.go"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, payload)
	}
	var outline symbols.Outline
	if err := json.Unmarshal(payload, &outline); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if indexer.asked != "internal/a.go" {
		t.Errorf("the store was asked for %q", indexer.asked)
	}
	// The limit comes from the outliner, so the two cannot drift apart into a file
	// the store will read and the parser will then refuse.
	if indexer.askedLimit != outliner.MaxBytes() {
		t.Errorf("limit = %d, want the outliner's %d", indexer.askedLimit, outliner.MaxBytes())
	}
	if len(outline.Symbols) != 1 || outline.Symbols[0].Name != "Needle" {
		t.Errorf("symbols = %+v", outline.Symbols)
	}
	if outline.Digest != "abc" {
		t.Errorf("digest = %q, want the store's", outline.Digest)
	}
}

// The store's refusals must arrive with their own status and code. Collapsing them
// into one failure would leave a caller unable to tell a path it can fix from a
// file that is not there.
func TestTheStoresRefusalKeepsItsCodeAndStatus(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"an escaping path", &searchindex.Error{Code: searchindex.CodeInvalidPath,
			Message: "the path must stay inside the project"}, http.StatusBadRequest, searchindex.CodeInvalidPath},
		{"a path the project does not hold", &searchindex.Error{Code: searchindex.CodeFileNotFound,
			Message: "no such file"}, http.StatusNotFound, searchindex.CodeFileNotFound},
		{"a file above the limit", &searchindex.Error{Code: searchindex.CodeFileTooLarge,
			Message: "too large"}, http.StatusBadRequest, searchindex.CodeFileTooLarge},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := newOutlineServer(t, &readableIndexer{err: c.err}, newOutliner(t))
			response, payload := postOutline(t, server, `{"path":"x"}`)

			if response.StatusCode != c.status {
				t.Errorf("status = %d, want %d: %s", response.StatusCode, c.status, payload)
			}
			var envelope errorEnvelope
			if err := json.Unmarshal(payload, &envelope); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if envelope.Error.Code != c.code {
				t.Errorf("code = %q, want %q", envelope.Error.Code, c.code)
			}
			if envelope.Error.Retryable {
				t.Error("the refusal is reported as retryable")
			}
		})
	}
}

func TestAnUnsupportedLanguageIsARefusalRatherThanAnEmptyOutline(t *testing.T) {
	server := newOutlineServer(t, &readableIndexer{content: []byte("# Title\n")}, newOutliner(t))

	response, payload := postOutline(t, server, `{"path":"README.md"}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.StatusCode, payload)
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error.Code != symbols.CodeUnsupportedLanguage {
		t.Errorf("code = %q", envelope.Error.Code)
	}
}

// A build with no symbol engine still serves every other route, and answers this
// one with a refusal rather than with an empty success.
func TestABuildWithoutASymbolEngineRefusesRatherThanAnswersEmptily(t *testing.T) {
	server := newOutlineServer(t, &readableIndexer{content: []byte("package p\n")}, nil)

	response, payload := postOutline(t, server, `{"path":"a.go"}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.StatusCode, payload)
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error.Code != symbols.CodeUnsupportedLanguage {
		t.Errorf("code = %q", envelope.Error.Code)
	}
}

func TestStatusNamesWhatTheBuildCanOutline(t *testing.T) {
	// A caller should learn the boundary by asking rather than by being refused on a
	// file, which is why the languages are on status and not only in a refusal.
	server := newOutlineServer(t, &readableIndexer{}, newOutliner(t))

	response, err := http.Get(server.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var view StatusView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if len(view.OutlineLanguages) == 0 {
		t.Fatal("status names no outline languages")
	}
	found := false
	for _, language := range view.OutlineLanguages {
		if language == symbols.LanguageGo {
			found = true
		}
	}
	if !found {
		t.Errorf("outline_languages = %v, want Go among them", view.OutlineLanguages)
	}
}

func TestStatusOmitsOutlineLanguagesWhenThereIsNoEngine(t *testing.T) {
	server := newOutlineServer(t, &readableIndexer{}, nil)

	response, err := http.Get(server.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var view StatusView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if len(view.OutlineLanguages) != 0 {
		t.Errorf("outline_languages = %v, want none", view.OutlineLanguages)
	}
}
