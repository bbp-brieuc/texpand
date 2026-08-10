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

// templateFuncs builds the function map made available to templates.
func templateFuncs(templateReadFromStdin bool) template.FuncMap {
	return template.FuncMap{
		"stdin": stdinFunc(templateReadFromStdin),
		"env":   envFunc(),
		"envOr": envOrFunc(),
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
	newMultistringFlag("e", "Define a template expansion key from an environment variable.  Format: <name> or <key>=<name>.  It is an error if the variable is not set.", func(s string) (string, error) {
		key, name := s, s
		if i := strings.IndexByte(s, '='); i >= 0 {
			key, name = s[:i], s[i+1:]
		}
		if key == "" || name == "" {
			return "", fmt.Errorf("invalid environment variable definition %q - it must be <name> or <key>=<name>", s)
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("the environment variable %q is not set", name)
		}
		dotMap[key] = value
		return s, nil
	})
	importEnv := flag.Bool("E", false, "Define a template expansion key for every environment variable.  Keys defined with -s and -e take precedence.")
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

Environment variables are available through the "env" and "envOr" template
functions, and can also be pulled into the dotmap with the -e and -E flags:
  $ echo 'home is {{env "HOME"}}, shell is {{envOr "SHELL" "none"}}' | %s
  $ echo 'user is {{.user}}' | %s -e user=USER
  $ echo 'user is {{.USER}}' | %s -E
"env" returns an empty string for an unset variable, "envOr" returns its second
argument, and -e fails when the variable is not set.
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
		flag.PrintDefaults()
		os.Exit(0)
	}

	if *importEnv {
		for _, kv := range os.Environ() {
			i := strings.IndexByte(kv, '=')
			if i < 0 {
				continue
			}
			// -s and -e have already filled the dotmap, and take precedence.
			if _, ok := dotMap[kv[:i]]; !ok {
				dotMap[kv[:i]] = kv[i+1:]
			}
		}
	}

	var t *template.Template
	var err error
	if a := flag.Args(); len(a) > 0 {
		funcs := templateFuncs(false)
		// The template returned by ParseFiles is named after the first file, so
		// the root template must use that name for Execute to run it.
		t, err = template.New(filepath.Base(a[0])).Funcs(funcs).ParseFiles(a...)
	} else {
		t, err = parseReader(os.Stdin, "stdin", templateFuncs(true))
	}
	if err != nil {
		die(1, "%s", err)
	}
	if err = t.Execute(os.Stdout, dotMap); err != nil {
		die(1, "%s", err)
	}
}
