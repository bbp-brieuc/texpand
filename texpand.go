package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
)

// stdinFunc returns the "stdin" template function.  It drains stdin on the
// first call and returns the same content on any subsequent call.  When the
// template itself was read from stdin, stdin has already been consumed and the
// function reports an error instead.
func stdinFunc(templateReadFromStdin bool) func() (string, error) {
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
		})
		return content, err
	}
}

// envFunc returns the "env" template function, which returns the value of an
// environment variable, or an empty string when it is not set.
func envFunc() func(string) string {
	return os.Getenv
}

// envOrFunc returns the "envOr" template function, which returns the value of
// an environment variable, or the given fallback when it is unset or empty.
func envOrFunc() func(string, string) string {
	return func(name, fallback string) string {
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return v
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
func templateFuncs(templateReadFromStdin bool, args []string) template.FuncMap {
	return template.FuncMap{
		"stdin": stdinFunc(templateReadFromStdin),
		"env":   envFunc(),
		"envOr": envOrFunc(),
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

func parseReader(r io.Reader, description string, funcs template.FuncMap) (*template.Template, error) {
	templateBytes, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("error when reading from %s - %w", description, err)
	}
	t, err := template.New(description).Funcs(funcs).Parse(string(templateBytes))
	if err != nil {
		return nil, fmt.Errorf("error when parsing template read from %s - %w", description, err)
	}
	return t, nil
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

`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		os.Exit(0)
	}

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
		t, err = template.New(filepath.Base(*script)).Funcs(templateFuncs(false, a)).Parse(body)
		if err != nil {
			err = fmt.Errorf("error when parsing the template read from %s - %w", *script, err)
		}
	case len(a) > 0:
		funcs := templateFuncs(false, nil)
		// The template returned by ParseFiles is named after the first file, so
		// the root template must use that name for Execute to run it.
		t, err = template.New(filepath.Base(a[0])).Funcs(funcs).ParseFiles(a...)
	default:
		t, err = parseReader(os.Stdin, "stdin", templateFuncs(true, nil))
	}
	if err != nil {
		die(1, "%s%s", err, note)
	}
	if err = t.Execute(os.Stdout, dotMap); err != nil {
		die(1, "%s%s", err, note)
	}
}
