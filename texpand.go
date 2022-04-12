package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"text/template"
)

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

func parseReader(r io.Reader, description string) (*template.Template, error) {
	templateBytes, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("error when reading from %s - %w", description, err)
	}
	t, err := template.New(description).Parse(string(templateBytes))
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
`, os.Args[0], os.Args[0], os.Args[0])
		flag.PrintDefaults()
		os.Exit(0)
	}

	var t *template.Template
	var err error
	if a := flag.Args(); len(a) > 0 {
		t, err = template.ParseFiles(a...)
	} else {
		t, err = parseReader(os.Stdin, "stdin")
	}
	if err != nil {
		die(1, "%s", err)
	}
	if err = t.Execute(os.Stdout, dotMap); err != nil {
		die(1, "%s", err)
	}
}
