Wire
------
[![Go Report Card](https://goreportcard.com/badge/github.com/wirekit/wire)](https://goreportcard.com/report/github.com/wirekit/wire)
[![Travis CI](https://travis-ci.org/wirekit/wire.svg?master=branch)](https://travis-ci.org/wirekit/wire)
[![Circle CI](https://circleci.com/gh/wirekit/wire.svg?style=svg)](https://circleci.com/gh/wirekit/wire)

Wire provides a tcp+websocket packages with blazing fast write speeds. It is a foundation upon which larger and more complex network applications can be built on.

## Install

```
go get -v github.com/wirekit/wire/...
```

## Protocols Implemented

### TCP

Wire provides the [mtcp](./mtcp) package which implements a lightweight tcp server and client implementations with blazing fast data transfers by combining minimal data copy with data buffering techniques. 

Mtcp like all Wire packages are a foundation, in that they lay the necessary foundation to transfer at blazing speed without being too opinionated on how you build on top.


See [MTCP](./mtcp) for more.

### Websockets

Wire provides the [msocks](./msocks) package which implements a lightweight websocket server and client implementations with blazing fast data transfers by combining minimal data copy with data buffering techniques. 

Msocks like all Wire packages are a foundation, in that they lay the necessary foundation to transfer at blazing speed without being too opinionated on how you build on top.


## Contributions

1. Fork this repository to your own GitHub account and then clone it to your local device
2. Make your changes with clear git commit messages.
3. Create a PR request with detail reason for change.

Do reach out anytime, if PR takes time for review. :)

## Vendoring
Vendoring was done with [Govendor](https://github.com/kardianos/govendor).
