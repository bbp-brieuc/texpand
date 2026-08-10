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

func main() {
	help := flag.Bool("h", false, "Print this help and exit.")
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
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
		flag.PrintDefaults()
		os.Exit(0)
	}

	var t *template.Template
	var err error
	if a := flag.Args(); len(a) > 0 {
		funcs := template.FuncMap{"stdin": stdinFunc(false)}
		// The template returned by ParseFiles is named after the first file, so
		// the root template must use that name for Execute to run it.
		t, err = template.New(filepath.Base(a[0])).Funcs(funcs).ParseFiles(a...)
	} else {
		t, err = parseReader(os.Stdin, "stdin", template.FuncMap{"stdin": stdinFunc(true)})
	}
	if err != nil {
		die(1, "%s", err)
	}
	if err = t.Execute(os.Stdout, dotMap); err != nil {
		die(1, "%s", err)
	}
}
