package http1

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func newChunkedWriter(w *bufio.Writer) io.WriteCloser {
	return &chunkedWriter{w}
}

type chunkedWriter struct {
	w *bufio.Writer
}

func (cw *chunkedWriter) Write(data []byte) (n int, err error) {
	if len(data) == 0 {
		return 0, nil
	}
	if _, err = fmt.Fprintf(cw.w, "%x\r\n", len(data)); err != nil {
		return 0, err
	}
	if _, err = cw.w.Write(data); err != nil {
		return
	}
	if _, err = io.WriteString(cw.w, "\r\n"); err != nil {
		return
	}
	return len(data), cw.w.Flush()
}

func (cw *chunkedWriter) Close() error {
	_, err := io.WriteString(cw.w, "0\r\n\r\n")
	return err
}

func newChunkedReader(r *bufio.Reader) io.ReadCloser {
	return &chunkedReaders{r: r}
}

type chunkedReaders struct {
	r       *bufio.Reader
	trailer http.Header
	i       uint64
	end     bool
}

func (cr *chunkedReaders) Read(data []byte) (n int, err error) {
	dataL := len(data)
	if dataL == 0 {
		return 0, nil
	}
	err = cr.initLen()
	if err != nil {
		return 0, err
	}
	if dataL > int(cr.i) {
		n, err = cr.r.Read(data[:cr.i])
	} else {
		n, err = cr.r.Read(data)
	}
	cr.i -= uint64(n)
	if err != nil {
		return n, err
	}
	if cr.i == 0 {
		_, err = readCRLFWithBytes(cr.r)
	}
	return n, err
}
func (cr *chunkedReaders) initLen() (err error) {
	if cr.i != 0 {
		return nil
	}
	if cr.end {
		return io.EOF
	}
	con, err := readCRLFWithBytes(cr.r)
	if err != nil {
		return err
	}
	cr.i, err = parseHexUint(bytes.TrimRight(con, "\r\n"))
	if err != nil {
		return errors.New("invalid chunked encoding")
	}
	if cr.i == 0 {
		cr.end = true
		err = cr.readTrailer()
		if err != nil {
			return err
		}
		return io.EOF
	}
	return nil
}
func (cr *chunkedReaders) readTrailer() (err error) {
	for {
		line, err := readCRLFWithString(cr.r)
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return nil
		}
		key, val, err := readHeadersWithKv(line)
		if err != nil {
			return err
		}
		if cr.trailer == nil {
			cr.trailer = make(http.Header)
		}
		cr.trailer.Add(key, val)
	}
}
func (cr *chunkedReaders) Close() error {
	return nil
}
func parseHexUint(v []byte) (n uint64, err error) {
	if len(v) == 0 {
		return 0, errors.New("empty byte array")
	}
	for _, b := range v {
		switch {
		case '0' <= b && b <= '9':
			b = b - '0'
		case 'a' <= b && b <= 'f':
			b = b - 'a' + 10
		case 'A' <= b && b <= 'F':
			b = b - 'A' + 10
		default:
			return 0, errors.New("invalid byte in chunk length")
		}
		n <<= 4
		n |= uint64(b)
	}
	return
}
