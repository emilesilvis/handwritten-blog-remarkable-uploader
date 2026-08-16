# Contributing

Bug reports and focused pull requests are welcome. Keep notebook contents,
device tokens, private signing keys, and device passwords out of issues, test
fixtures, commits, and build logs.

Before opening a pull request, run:

```sh
gofmt -w *.go
go test -race ./...
go vet ./...
scripts/build
```

Changes to snapshot behavior, supported firmware, authentication, or package
lifecycle hooks require tests and a real-device validation plan.
