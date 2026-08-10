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
