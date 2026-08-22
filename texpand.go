package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"
)

// recorder collects what an invocation consumed beyond its template files, so
// that it can be replayed later: the environment variables actually expanded
// and the content of stdin when it was read.  A nil recorder is valid and
// records nothing, which is the case unless TEXPAND_LOG is set.
type recorder struct {
	path  string
	env   map[string]string
	unset map[string]bool
	stdin *string
}

// newRecorder returns a recorder appending to the file named by the TEXPAND_LOG
// environment variable, or nil when it is empty.
func newRecorder() *recorder {
	path := os.Getenv("TEXPAND_LOG")
	if path == "" {
		return nil
	}
	return &recorder{path: path, env: make(map[string]string), unset: make(map[string]bool)}
}

// recordEnv records one environment variable lookup made by the template.
func (r *recorder) recordEnv(name, value string, set bool) {
	if r == nil {
		return
	}
	if set {
		r.env[name] = value
	} else {
		r.unset[name] = true
	}
}

// recordStdin records the content read from stdin, whether it was the template
// itself or the value of the "stdin" template function.
func (r *recorder) recordStdin(content string) {
	if r == nil {
		return
	}
	r.stdin = &content
}

// write appends the invocation record as one JSON line to the log file.  The
// record holds everything the invocation consumed which is not in the template
// files: argv, the working directory, the environment variables the template
// expanded (unset ones listed apart, so a replay knows to unset them), stdin
// when it was read, and the error when the invocation failed.
func (r *recorder) write(failure error) error {
	if r == nil {
		return nil
	}
	entry := struct {
		Time     string            `json:"time"`
		Cwd      string            `json:"cwd,omitempty"`
		Argv     []string          `json:"argv"`
		Env      map[string]string `json:"env,omitempty"`
		UnsetEnv []string          `json:"unset_env,omitempty"`
		Stdin    *string           `json:"stdin,omitempty"`
		Error    string            `json:"error,omitempty"`
	}{
		Time:  time.Now().Format(time.RFC3339),
		Argv:  os.Args,
		Env:   r.env,
		Stdin: r.stdin,
	}
	// The directory matters to a replay because argv may hold relative paths;
	// not knowing it is not worth failing the log.
	entry.Cwd, _ = os.Getwd()
	for name := range r.unset {
		entry.UnsetEnv = append(entry.UnsetEnv, name)
	}
	sort.Strings(entry.UnsetEnv)
	if failure != nil {
		entry.Error = failure.Error()
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// stdinFunc returns the "stdin" template function.  It drains stdin on the
// first call and returns the same content on any subsequent call.  When the
// template itself was read from stdin, stdin has already been consumed and the
// function reports an error instead.
func stdinFunc(templateReadFromStdin bool, rec *recorder) func() (string, error) {
	var (
		once    sync.Once
		content string
		err     error
	)
	return func() (string, error) {
		if templateReadFromStdin {
			return "", fmt.Errorf("the stdin function is not available when the template itself is read from stdin")
		}
		once.Do(func() {
			var b []byte
			if b, err = ioutil.ReadAll(os.Stdin); err != nil {
				err = fmt.Errorf("error when reading from stdin - %w", err)
				return
			}
			content = string(b)
			rec.recordStdin(content)
		})
		return content, err
	}
}

// envFunc returns the "env" template function, which returns the value of an
// environment variable, or an empty string when it is not set.
func envFunc(rec *recorder) func(string) string {
	return func(name string) string {
		value, set := os.LookupEnv(name)
		rec.recordEnv(name, value, set)
		return value
	}
}

// envOrFunc returns the "envOr" template function, which returns the value of
// an environment variable, or the given fallback when it is unset or empty.
func envOrFunc(rec *recorder) func(string, string) string {
	return func(name, fallback string) string {
		value, set := os.LookupEnv(name)
		rec.recordEnv(name, value, set)
		if set && value != "" {
			return value
		}
		return fallback
	}
}

// argsFunc returns the "args" template function, which returns the command line
// arguments following the script file.
func argsFunc(args []string) func() []string {
	return func() []string { return args }
}

// templateFuncs builds the function map made available to templates.
func templateFuncs(templateReadFromStdin bool, args []string, rec *recorder) template.FuncMap {
	return template.FuncMap{
		"stdin": stdinFunc(templateReadFromStdin, rec),
		"env":   envFunc(rec),
		"envOr": envOrFunc(rec),
		"args":  argsFunc(args),
	}
}

type multistringFlag struct {
	values []string
	parse  func(string) (string, error)
}

func newMultistringFlag(name, usage string, parse func(string) (string, error)) *multistringFlag {
	m := &multistringFlag{parse: parse}
	if m.parse == nil {
		m.parse = func(s string) (string, error) { return s, nil }
	}
	flag.Var(m, name, usage)
	return m
}

// String pretty prints the multistringFlag.
func (m *multistringFlag) String() string { return fmt.Sprintf("%q", m.values) }

// Set adds a value to a multistringFlag; it implements the flag.Value interface.
func (m *multistringFlag) Set(value string) error {
	v, err := m.parse(value)
	if err != nil {
		return err
	}
	m.values = append(m.values, v)
	return nil
}

func die(code int, format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(code)
}

// headerDelimiter is the line delimiting the header of a script file, both
// before and after it.
const headerDelimiter = "---"

// parseDefinition reads a <key>=<value> definition from a header line.  The key
// must be a letter or an underscore followed by letters, digits or underscores;
// any other line is not a definition.  The value is the rest of the line, kept
// verbatim.
func parseDefinition(line string) (string, string, bool) {
	i := strings.IndexByte(line, '=')
	if i <= 0 {
		return "", "", false
	}
	key := line[:i]
	for j := 0; j < len(key); j++ {
		switch c := key[j]; {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case j > 0 && c >= '0' && c <= '9':
		default:
			return "", "", false
		}
	}
	return key, line[i+1:], true
}

// splitScript splits the content of a script file into the values defined by
// its header and its template.  A leading shebang line is dropped, so that the
// file can be run by the kernel through a "#!" line.  The header is optional;
// when present, both its first and last lines contain exactly "---", and
// everything preceding it is ignored, whatever it contains, so that the file can
// also be a script for another interpreter.  Header lines which are not
// <key>=<value> definitions are ignored.  A file with no "---" line, or whose
// first "---" line is not followed by another one, has no header: all of it,
// save its shebang line, is the template.  The returned count is the number of
// lines preceding the template, which the caller needs to map the line numbers
// reported by text/template, relative to the template, back to the script file.
func splitScript(content string) (map[string]string, string, int) {
	// SplitAfter keeps the line terminators, so that the template section can be
	// rebuilt exactly as it was written.
	lines := strings.SplitAfter(content, "\n")
	trimmed := func(i int) string { return strings.TrimSuffix(lines[i], "\n") }
	start := 0
	if len(lines) > 0 && strings.HasPrefix(lines[0], "#!") {
		start = 1
	}
	for i := start; i < len(lines); i++ {
		if trimmed(i) != headerDelimiter {
			continue
		}
		values := make(map[string]string)
		for j := i + 1; j < len(lines); j++ {
			if trimmed(j) == headerDelimiter {
				return values, strings.Join(lines[j+1:], ""), j + 1
			}
			if key, value, ok := parseDefinition(trimmed(j)); ok {
				values[key] = value
			}
		}
		// The header is never closed, so it isn't one: the values it seemed to
		// define are dropped and the whole file is the template.
		break
	}
	return make(map[string]string), strings.Join(lines[start:], ""), start
}

func main() {
	help := flag.Bool("h", false, "Print this help and exit.")
	script := flag.String("f", "", "Read the template from the given script file: its shebang line is dropped, everything preceding its optional '---' delimited header is ignored, that header defines values, and the remaining arguments are passed to the script instead of being read as templates.")
	dotMap := make(map[string]string)
	newMultistringFlag("s", "Define a string value associated to a template expansion key.  Format: <key>=<value>.", func(s string) (string, error) {
		i := strings.IndexByte(s, '=')
		if i < 0 {
			return "", fmt.Errorf("invalid key/value definition %q - it must contain an '=' sign", s)
		}
		dotMap[s[:i]] = s[i+1:]
		return s, nil
	})
	flag.CommandLine.Init("", flag.ContinueOnError)
	flag.Usage = func() {}
	if flag.CommandLine.Parse(os.Args[1:]) != nil {
		die(2, "run with -h for help")
	}
	if *help {
		fmt.Fprintf(os.Stderr, `%s - read a text template and prints it after expanding its content

Usage: %s [<options>] [template file 1 [template file 2 [...]]]

Example:
  $ echo 'foo is {{.foo}}, bar is {{.bar}}' | %s -s foo=oof -s bar=rab
  foo is oof, bar is rab

The template is read from stdin, or from files passed as command line arguments.
Its syntax is that of the golang text/template package, documented at https://pkg.go.dev/text/template and it's executed with the dot pointing to a map of string key/values, named the dotmap, defined using the -s flag.

When the template is read from files, the extra "stdin" template function reads
all of stdin and returns it as a string:
  $ echo world | %s -s greeting=Hello <(echo '{{.greeting}} {{stdin}}')
  Hello world
It can be called several times, always returning the same content, and it is an
error to use it when the template itself is read from stdin.

Any environment variable is available through the "env" and "envOr" template
functions, without having to declare it on the command line:
  $ echo 'home is {{env "HOME"}}, shell is {{envOr "SHELL" "none"}}' | %s
"env" returns an empty string for an unset variable, while "envOr" returns its
second argument when the variable is unset or empty.

The -f option names one script file, which is read instead of the templates.
Its first line is dropped when it is a "#!" line, so that the script can be run
by the kernel, and it may contain a header delimited by two "---" lines, whose
<key>=<value> lines define values for the expansion; any other header line is
ignored, and so is everything preceding the header.  The remaining command line
arguments are not read as templates, they are returned by the "args" template
function:
  $ cat hello
  #!/usr/local/bin/%s -f
  ---
  greeting=Hello
  ---
  {{.greeting}} {{index args 0}}
  $ ./hello world
  Hello world
Values defined by the header are overridden by those given with the -s flag.

When the TEXPAND_LOG environment variable is not empty, it names a file where
each invocation appends one JSON line recording what it consumed beyond the
template files, so that anyone holding those files can reproduce the output:
argv, the working directory, the environment variables the template actually
expanded (the unset ones listed apart), stdin when it was read, and the error
when the invocation failed.

`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		os.Exit(0)
	}

	rec := newRecorder()
	var t *template.Template
	var err error
	// text/template reports line numbers relative to what it parsed, which for a
	// script file is its template alone: the note tells how to map them back to
	// the file.  It is empty when both are the same thing.
	var note string
	a := flag.Args()
	switch {
	case *script != "":
		var content []byte
		if content, err = ioutil.ReadFile(*script); err != nil {
			die(1, "error when reading %s - %s", *script, err)
		}
		values, body, skipped := splitScript(string(content))
		if skipped > 0 {
			note = fmt.Sprintf("\nnote: line N of the template is line N+%d of %s", skipped, *script)
		}
		// The values given on the command line take precedence over those
		// defined by the script header.
		for key, value := range dotMap {
			values[key] = value
		}
		dotMap = values
		t, err = template.New(filepath.Base(*script)).Funcs(templateFuncs(false, a, rec)).Parse(body)
		if err != nil {
			err = fmt.Errorf("error when parsing the template read from %s - %w", *script, err)
		}
	case len(a) > 0:
		funcs := templateFuncs(false, nil, rec)
		// The template returned by ParseFiles is named after the first file, so
		// the root template must use that name for Execute to run it.
		t, err = template.New(filepath.Base(a[0])).Funcs(funcs).ParseFiles(a...)
	default:
		var b []byte
		if b, err = ioutil.ReadAll(os.Stdin); err != nil {
			err = fmt.Errorf("error when reading from stdin - %w", err)
			break
		}
		rec.recordStdin(string(b))
		if t, err = template.New("stdin").Funcs(templateFuncs(true, nil, rec)).Parse(string(b)); err != nil {
			err = fmt.Errorf("error when parsing template read from stdin - %w", err)
		}
	}
	if err == nil {
		err = t.Execute(os.Stdout, dotMap)
	}
	if werr := rec.write(err); werr != nil {
		fmt.Fprintf(os.Stderr, "warning: error when writing the invocation log to %s - %s\n", rec.path, werr)
	}
	if err != nil {
		die(1, "%s%s", err, note)
	}
}
