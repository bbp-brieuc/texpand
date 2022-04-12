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
