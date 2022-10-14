# chrome2spall (a hack)

This is a tool to convert Chrome performance profiles to a format that works with [spall]9https://github.com/colrdavidson/spall).

## Install

Requires Go 1.19 or higher.

```
go install github.com/bvisness/chrome2spall@latest
```

## Run

```
chrome2spall myprofile.json > out.json
```
