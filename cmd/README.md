The steps taken when building any program in this directory:
```
$ go fmt
$ go vet
$ golint
$ gosec ./
$ CGO_ENABLED=0 go build -tags=netgo
```

While `-tags=netgo` tends to be enough to get a static binary when `net/http`
is involved, I have noticed `github.com/lib/pq` requires `CGO_ENABLED=0`.
