# recv.sh

Easy data receiving

## Install

```shell
go get github.com/six-ddc/recv.sh
```

## Usage

```shell
recv.sh: error: required argument '[host]:port' not provided

usage: recv.sh [<flags>] <[host]:port> [<file>]

Flags:
  -h, --help          Show context-sensitive help (also try --help-long and --help-man).
  -z, --gzip          Accept gzipped data
  -a, --append        Append data to the output file when writing
  -m, --mutex         Read data one by one
  -c, --chunk         Read data in chunk mode, default (line mode)
  -u, --udp           Use udp instead of the default option of tcp
      --bufsize=64KB  Sepcify read buffer size on udp
  -v, --verbose       Verbose
      --version       Show application version.

Args:
  <[host]:port>  Listening address
  [<file>]       Specify output file name, support Go template, i.e. 'out-{{.Id}}-{{.Ip}}-{{.Port}}'
```

## Example

* Receiver

```shell
recv.sh :8080 outputs.txt
# recv.sh :8080 outputs-{{.Ip}}.txt
```

* Sender

```shell
# machine A
xxx | nc 127.0.0.1 8080

# machine B
xxx | nc 127.0.0.1 8080
```
