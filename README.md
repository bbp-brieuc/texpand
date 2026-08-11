# texpand
Command line tool to expand text templates, replacing some patterns with user defined values.

For example, if `template.txt` contains:
```
Hello {{.name}}, I'm {{.me}},
nice to meet you!
```

then it can be expanded as follows:
```
$ texpand -s name=Cathy -s me=Bob template.txt
Hello Cathy, I'm Bob,
nice to meet you!
```

The template syntax is that of golang [text/template](https://pkg.go.dev/text/template) package.

## Environment variables

Any environment variable can be used directly in the template with the `env` and
`envOr` functions, with nothing to declare on the command line:
```
$ echo 'Hi {{env "USER"}}, your editor is {{envOr "EDITOR" "vi"}}' | texpand
Hi bob, your editor is vi
```
`env` returns an empty string when the variable is not set. `envOr` returns its
second argument when the variable is unset or empty.

## The `stdin` function

When the template comes from files, the `stdin` template function drains stdin
and inserts its content in the expansion:
```
$ echo 'nice to meet you!' > greeting.txt
$ cat template.txt
Hello {{.name}}, I'm {{.me}},
{{stdin}}
$ cat greeting.txt | texpand -s name=Cathy -s me=Bob template.txt
Hello Cathy, I'm Bob,
nice to meet you!
```

Stdin is read once; calling `stdin` again returns the same content. It is an
error to use it when the template itself is read from stdin, since stdin is
already consumed by the template.

## Script files

The `-f <file>` option names one script file, which texpand reads instead of the
templates, which lets it be used as an interpreter on a `#!` line:
```
$ cat hello
#!/usr/local/bin/texpand -f
---
greeting=Hello
Everything which is not a <key>=<value> line here is ignored,
so this paragraph is just a comment.
---
{{.greeting}} {{index args 0}}!
$ chmod +x hello
$ ./hello world
Hello world!
```

Exactly one file is read as a script, the one given to `-f`; the option is not
repeatable, and the other arguments are never read as templates.

The file is made of three parts, and only the last one is mandatory:
- a `#!` first line, which is dropped;
- a header delimited by two lines containing exactly `---`, whose
  `<key>=<value>` lines define values for the expansion, every other line being
  ignored;
- the template itself.

Everything preceding the header is ignored, whatever it contains, so the same
file can be a script for another interpreter which calls texpand on itself:
```
#! /bin/sh

texpand -f "$0" "$@" | some-other-command

exit 0

---
greeting=Hello
---
{{.greeting}} {{index args 0}}!
```

A file with no `---` line, or whose first `---` line is not followed by another
one, has no header: all of it, save its `#!` line, is the template.

A key is a letter or an underscore followed by letters, digits or underscores;
a value is the rest of the line, kept verbatim, so `foo = bar` defines nothing
and `foo=  bar` sets `foo` to `  bar`. Values given with `-s` on the command
line override those of the header.

The arguments following the script file are not read as templates: they are
passed to the script, which reads them with the `args` function, either by
index as above or with `{{range args}}`.

Note that `#!/usr/bin/env texpand -f` does not work on Linux, where the kernel
passes everything following the interpreter path as a single argument: use the
absolute path of texpand, as above, or `#!/usr/bin/env -S texpand -f` on the
systems whose `env` supports `-S`.
