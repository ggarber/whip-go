# whip-go

whip-go is a simple WHIP (https://datatracker.ietf.org/doc/draft-ietf-wish-whip/) client implementation in go using the WebRTC [Pion libraries](https://github.com/pion).
It includes a WHIPClient class and a simple command line client supporrting screensharing to a WHIP ingestion endpoint.

It has been tested with [janus-gateway](https://github.com/meetecho/janus-gateway) with [whip-simple-server](https://github.com/lminiero/simple-whip-server).

## Installation

```
go build
```

## Running

```
./whip-go -v VIDEO_SOURCE -t TOKEN WHIP_ENDPOINT_URL
```

The supported sources are either "screen" or the name of a file (f.e. "/dev/stdin") to ready raw YUV420 samples from.

For more information run:
```
./whip-go -h
```