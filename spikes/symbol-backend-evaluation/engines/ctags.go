package engines

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Ctags extracts symbols through Universal Ctags.
//
// Two ways of driving it are implemented because they behave differently, and the
// difference is a result rather than a detail.
//
// ModeInteractive keeps one warm process and writes each file's content to it.
// That is how Zoekt drives ctags and is the configuration a production service
// would want, because it pays for process creation once. It is also the one that
// stalls: see the results document.
//
// ModePerFile runs one process per file against a path on disk. It costs a fork
// each time and it can be bounded, because abandoning one file means killing a
// process that was going to exit anyway.
type Ctags struct {
	mode    Mode
	binary  string
	timeout time.Duration

	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	// languages is the configured set, kept to the languages under evaluation so
	// the two candidates answer about the same corpus.
	languages map[string]string
	closed    bool
}

// Mode selects how ctags is driven.
type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModePerFile     Mode = "per-file"
)

// ctagsLanguageNames maps the harness's language name onto the name Universal
// Ctags uses, which differs in case and punctuation.
var ctagsLanguageNames = map[string]string{
	"go":         "Go",
	"python":     "Python",
	"rust":       "Rust",
	"c":          "C",
	"cpp":        "C++",
	"javascript": "JavaScript",
	"typescript": "TypeScript",
	"tsx":        "TypeScript",
}

// ctagsFields is the same field set in both modes, so the two are comparable.
var ctagsFields = []string{"--fields=+neKzS", "--sort=no"}

// NewCtags builds the engine in the given mode.
func NewCtags(binary string, mode Mode, timeout time.Duration) (*Ctags, error) {
	if binary == "" {
		binary = "ctags"
	}
	if mode == "" {
		mode = ModePerFile
	}
	engine := &Ctags{binary: binary, mode: mode, timeout: timeout, languages: ctagsLanguageNames}
	if mode == ModePerFile {
		// Nothing is started until a file arrives, but the executable's absence
		// should be reported now rather than as 933 identical per-file failures.
		if _, err := exec.LookPath(binary); err != nil {
			return nil, err
		}
		return engine, nil
	}
	return engine.startInteractive()
}

func (c *Ctags) startInteractive() (*Ctags, error) {
	command := exec.Command(c.binary, append([]string{"--_interactive=default"}, ctagsFields...)...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", c.binary, err)
	}

	c.command = command
	c.stdin = stdin
	c.stdout = bufio.NewReaderSize(stdout, 1<<20)

	// The process announces itself before accepting a request. Reading the
	// announcement is also the check that the interactive protocol exists at all
	// in this build.
	line, err := c.stdout.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read the ctags greeting: %w", err)
	}
	var greeting struct {
		Type    string `json:"_type"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(line, &greeting); err != nil || greeting.Type != "program" {
		return nil, fmt.Errorf("the ctags build does not speak the interactive protocol: %s", strings.TrimSpace(string(line)))
	}
	return c, nil
}

func (c *Ctags) Name() string { return "universal-ctags (" + string(c.mode) + ")" }

func (c *Ctags) Capabilities() Capabilities {
	return Capabilities{
		// Tags carry a line and, for some kinds, an end line. Nothing in the tag
		// format bounds a declaration in bytes.
		ByteRanges: false,
		// scope and scopeKind name the enclosing declaration for most languages.
		Containment: true,
		// A tag generator reports what it recognized. There is no failure to
		// report: an unparseable file yields fewer tags, which is indistinguishable
		// from a file that declares less.
		SyntaxValidity: false,
		InProcess:      false,
		License:        "GPL-2.0 (separate executable in the image)",
	}
}

func (c *Ctags) Languages() []string {
	names := make([]string, 0, len(c.languages))
	for name := range c.languages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Ctags) Close() {
	if c.closed || c.command == nil {
		return
	}
	c.closed = true
	_ = c.stdin.Close()
	_ = c.command.Wait()
}

type ctagsTag struct {
	Type      string `json:"_type"`
	Name      string `json:"name"`
	Line      int    `json:"line"`
	End       int    `json:"end"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	ScopeKind string `json:"scopeKind"`
	Command   string `json:"command"`
	Message   string `json:"message"`
}

func (c *Ctags) Analyse(request Request, content []byte) FileResult {
	result := FileResult{Path: request.Path, Language: request.Language, Bytes: len(content)}
	forced, known := c.languages[request.Language]
	if !known {
		result.Err = "no ctags language configured for " + request.Language
		return result
	}
	// A tag generator has no concept of a broken file, so it reports every file as
	// whole. Recording true here would be a claim the engine did not make; the
	// capability flag is what says the answer is unavailable.
	result.Parsed = true

	if c.mode == ModePerFile {
		return c.analysePerFile(result, request, forced, content)
	}
	return c.analyseInteractive(result, forced, content)
}

// analysePerFile runs one bounded process against a path on disk.
//
// Universal Ctags rejects `-` as an input file, so there is no way to hand it
// source it cannot open. When the caller has only content, a temporary file is
// written; that write is included in the measurement because a production
// integration would have to do it too.
func (c *Ctags) analysePerFile(result FileResult, request Request, forced string, content []byte) FileResult {
	target := request.Full
	if target == "" {
		temporary, err := os.CreateTemp("", "ctags-*"+filepath.Ext(request.Path))
		if err != nil {
			result.Err = err.Error()
			return result
		}
		defer os.Remove(temporary.Name())
		if _, err := temporary.Write(content); err != nil {
			temporary.Close()
			result.Err = err.Error()
			return result
		}
		temporary.Close()
		target = temporary.Name()
	}

	ctx := context.Background()
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	arguments := append([]string{"-f", "-", "--output-format=json", "--language-force=" + forced}, ctagsFields...)
	command := exec.CommandContext(ctx, c.binary, append(arguments, target)...)
	command.Stderr = io.Discard

	out, err := command.Output()
	if ctx.Err() != nil {
		result.TimedOut = true
		return result
	}
	if err != nil && len(out) == 0 {
		result.Err = err.Error()
		return result
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var tag ctagsTag
		if err := json.Unmarshal(line, &tag); err != nil || tag.Type != "tag" {
			continue
		}
		result.Symbols = append(result.Symbols, symbolFromTag(tag))
	}
	return result
}

func (c *Ctags) analyseInteractive(result FileResult, forced string, content []byte) FileResult {
	path := result.Path
	request := struct {
		Command  string `json:"command"`
		Filename string `json:"filename"`
		Size     int    `json:"size"`
		Language string `json:"language-force"`
	}{Command: "generate-tags", Filename: path, Size: len(content), Language: forced}

	encoded, err := json.Marshal(request)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		result.Err = "write the request: " + err.Error()
		return result
	}
	if _, err := c.stdin.Write(content); err != nil {
		result.Err = "write the content: " + err.Error()
		return result
	}

	// The protocol has no way to abandon one file. A stalled reply can only be
	// escaped by killing the process, which is why the watchdog is here and why
	// recovering costs the warm process that was the reason to use this mode.
	var watchdog *time.Timer
	if c.timeout > 0 {
		watchdog = time.AfterFunc(c.timeout, func() {
			if c.command != nil && c.command.Process != nil {
				_ = c.command.Process.Kill()
			}
		})
	}

	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			if watchdog != nil && !watchdog.Stop() {
				result.TimedOut = true
				// The stream is now out of step with the requests even if the
				// process had survived, so it is replaced rather than reused.
				c.restartAfterKill()
				return result
			}
			result.Err = "read the reply: " + err.Error()
			return result
		}
		var tag ctagsTag
		if err := json.Unmarshal(line, &tag); err != nil {
			continue
		}
		switch tag.Type {
		case "completed":
			if watchdog != nil {
				watchdog.Stop()
			}
			return result
		case "error":
			if watchdog != nil {
				watchdog.Stop()
			}
			result.Err = tag.Message
			return result
		case "tag":
			result.Symbols = append(result.Symbols, symbolFromTag(tag))
		}
	}
}

// restartAfterKill replaces a process the watchdog killed.
func (c *Ctags) restartAfterKill() {
	_ = c.stdin.Close()
	_ = c.command.Wait()
	c.command, c.stdin, c.stdout = nil, nil, nil
	if _, err := c.startInteractive(); err != nil {
		c.closed = true
	}
}

func symbolFromTag(tag ctagsTag) Symbol {
	end := tag.End
	if end < tag.Line {
		end = tag.Line
	}
	return Symbol{
		Name:      tag.Name,
		Kind:      ctagsKind(tag.Kind),
		StartLine: tag.Line,
		EndLine:   end,
		Container: lastScopeComponent(tag.Scope),
	}
}

// lastScopeComponent reduces a dotted or double-colon scope path to the
// immediately enclosing name, which is what Container means.
func lastScopeComponent(scope string) string {
	if scope == "" {
		return ""
	}
	for _, separator := range []string{"::", ".", "/"} {
		if index := strings.LastIndex(scope, separator); index >= 0 {
			return scope[index+len(separator):]
		}
	}
	return scope
}

// ctagsKind maps the tag vocabulary onto the normalized one. Ctags kind names are
// per language and there are many; the ones that matter for the languages under
// evaluation are here and anything else becomes "other" rather than being guessed.
var ctagsKindNames = map[string]Kind{
	"function":     KindFunction,
	"func":         KindFunction,
	"method":       KindMethod,
	"methodSpec":   KindMethod,
	"class":        KindClass,
	"struct":       KindStruct,
	"interface":    KindInterface,
	"enum":         KindEnum,
	"enumerator":   KindConstant,
	"enumConstant": KindConstant,
	"typedef":      KindType,
	"type":         KindType,
	"alias":        KindType,
	"member":       KindField,
	"field":        KindField,
	"variable":     KindVariable,
	"var":          KindVariable,
	"constant":     KindConstant,
	"const":        KindConstant,
	"macro":        KindMacro,
	"module":       KindModule,
	"namespace":    KindModule,
	"package":      KindModule,
	"union":        KindStruct,
	"trait":        KindInterface,
	"prototype":    KindFunction,
	"property":     KindField,
}

func ctagsKind(name string) Kind {
	if kind, known := ctagsKindNames[name]; known {
		return kind
	}
	return KindOther
}
