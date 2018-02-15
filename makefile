ci: coverage tests benchmarks

coverage:
	go test -v -cover ./mtcp/...
	go test -v -cover ./mudp/...
	go test -v -cover ./msocks/...

tests:
    go test -v ./mtcp/...
    go test -v ./mudp/...
    go test -v ./msocks/...

benchmarks:
	go test -run=xXX -bench=. ./mtcp/...
	go test -run=xXX -bench=. ./mudp/...
	go test -run=xXX -bench=. ./msocks/...
