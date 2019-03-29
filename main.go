package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"gopkg.in/alecthomas/kingpin.v2"
	"io"
	"net"
	"os"
	"sync"
	"text/template"
)

var (
	addr    = kingpin.Arg("[host]:port", "Listening address").Required().String()
	file    = kingpin.Arg("file", "Specify output file name, support Go template, i.e. 'out-{{.Id}}-{{.Ip}}-{{.Port}}'").String()
	gz      = kingpin.Flag("gzip", "Accept gzipped data").Short('z').Bool()
	app     = kingpin.Flag("append", "Append data to the output file when writing").Short('a').Bool()
	mutex   = kingpin.Flag("mutex", "Read data one by one").Short('m').Bool()
	chunk   = kingpin.Flag("chunk", "Read data in chunk mode, default (line mode)").Short('c').Bool()
	udp     = kingpin.Flag("udp", "Use udp instead of the default option of tcp").Short('u').Bool()
	bufSize = kingpin.Flag("bufsize", "Sepcify read buffer size on udp").Default("64KB").Bytes()
	verbose = kingpin.Flag("verbose", "Verbose").Short('v').Bool()
)

var (
	handleMutex sync.Locker
	fileMap     map[string]*os.File
	id          int64
	tcpListener net.Listener
	udpListener net.PacketConn
)

type templateBinding struct {
	Ip   string
	Port int
	Id   int64
}

const maxLineLength = int(^uint(0)>>1) / 2

type fakeLocker struct{}

func (*fakeLocker) Lock()   {}
func (*fakeLocker) Unlock() {}

func main() {
	kingpin.CommandLine.HelpFlag.Short('h')
	kingpin.Version("1.0")
	_, err := kingpin.CommandLine.Parse(os.Args[1:])
	if err != nil {
		kingpin.CommandLine.FatalUsage("%s\n", err)
	}

	if *udp {
		udpListener, err = net.ListenPacket("udp", *addr)
		defer udpListener.Close()
	} else {
		tcpListener, err = net.Listen("tcp", *addr)
		defer tcpListener.Close()
	}
	if err != nil {
		exit(err)
	}

	if *mutex {
		handleMutex = &sync.Mutex{}
	} else {
		handleMutex = &fakeLocker{}
	}

	fileMap = make(map[string]*os.File, 1)

	var t *template.Template
	if *file != "" {
		t, err = checkTemplate(*file)
		if err != nil {
			exit(err)
		}
	}

	if *udp {
		log("Listening on %s\n", udpListener.LocalAddr())
		serveUdp(t)
	} else {
		log("Listening on %s\n", tcpListener.Addr())
		serveTcp(t)
	}
}

func checkTemplate(fileName string) (*template.Template, error) {
	t, err := template.New("fileName").Parse(fileName)
	if err != nil {
		return nil, err
	}

	buffer := bytes.NewBuffer([]byte{})
	err = t.Execute(buffer, &templateBinding{
		Id:   1,
		Ip:   "127.0.0.1",
		Port: 8080,
	})
	return t, err
}

func serveUdp(t *template.Template) {
	for {
		data := make([]byte, *bufSize)
		n, addr, err := udpListener.ReadFrom(data)
		if err != nil {
			exit(err)
		}
		id++

		outputFile := getOutputFile(t, err, addr)

		go func() {
			handleMutex.Lock()
			defer func() {
				handleMutex.Unlock()
			}()

			log("Read data from %s\n", addr)
			buf := make([]byte, n)
			copy(buf, data)
			reader := bytes.NewBuffer(buf)
			handleRequest(reader, addr, outputFile)
		}()
	}
}

func serveTcp(t *template.Template) {
	for {
		conn, err := tcpListener.Accept()
		if err != nil {
			exit(err)
		}
		id++

		outputFile := getOutputFile(t, err, conn.RemoteAddr())

		go func() {
			handleMutex.Lock()
			defer func() {
				handleMutex.Unlock()
				conn.Close()
			}()

			log("Read data from %s\n", conn.RemoteAddr())
			//reader := bufio.NewReader(conn)
			handleRequest(conn, conn.RemoteAddr(), outputFile)
		}()
	}
}

func getOutputFile(t *template.Template, err error, addr net.Addr) *os.File {
	fileName := *file
	if t != nil {
		buffer := bytes.NewBuffer([]byte{})
		var ip string
		var port int
		if *udp {
			ip = addr.(*net.UDPAddr).IP.String()
			port = addr.(*net.UDPAddr).Port
		} else {
			ip = addr.(*net.TCPAddr).IP.String()
			port = addr.(*net.TCPAddr).Port
		}
		err = t.Execute(buffer, &templateBinding{
			Id:   id,
			Ip:   ip,
			Port: port,
		})
		if err != nil {
			exit(err)
		}
		fileName = buffer.String()
		if fileName == *file {
			t = nil
		}
	}
	outputFile := os.Stdout
	if fileName != "" {
		outputFile = openOutputFile(fileName)
	}
	return outputFile
}

func openOutputFile(fileName string) *os.File {
	if file, ok := fileMap[fileName]; ok {
		return file
	}

	mode := os.O_CREATE | os.O_WRONLY
	if *app {
		mode |= os.O_APPEND
	}
	file, err := os.OpenFile(fileName, mode, 0644)
	if err != nil {
		exit(err)
	}
	fileMap[fileName] = file
	return file
}

func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[0 : i+1], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func handleRequest(reader io.Reader, addr net.Addr, file *os.File) {
	if *gz {
		peekReader := bufio.NewReader(reader)
		// ref: gunzip.readHeader
		header, _ := peekReader.Peek(10)
		_, e := gzip.NewReader(bytes.NewReader(header))
		if e == nil {
			reader, _ = gzip.NewReader(peekReader)
		} else {
			reader = peekReader
		}
	}
	if *chunk {
		handleRequestInChunk(reader, addr, file)
	} else {
		handleRequestInText(reader, addr, file)
	}
}

func handleRequestInChunk(reader io.Reader, addr net.Addr, file *os.File) {
	var written int64
	defer func() {
		log("Connection %s closed, read bytes %d\n", addr, written)
		handleMutex.Unlock()
	}()
	buf := make([]byte, 64*1024)
	written, err := io.CopyBuffer(file, reader, buf)
	if err != nil {
		log("Read error: %s\n", err.Error())
	}
}

func handleRequestInText(reader io.Reader, addr net.Addr, file *os.File) {
	scanner := bufio.NewScanner(reader)
	scanner.Split(scanLines)
	var buf []byte
	scanner.Buffer(buf, maxLineLength)

	var lines int64
	defer func() {
		log("Connection %s closed, read lines %d\n", addr, lines)
	}()
	for scanner.Scan() {
		file.Write(scanner.Bytes())
		lines++
	}
	if scanner.Err() != nil {
		log("Read error: %s\n", scanner.Err().Error())
	}
}

func log(format string, a ...interface{}) {
	if *verbose {
		fmt.Fprintf(os.Stderr, format, a...)
	}
}

func exit(a ...interface{}) {
	fmt.Fprintln(os.Stderr, a...)
	os.Exit(1)
}
