package http1

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gospider007/tools"
)

func readCRLFWithBytes(r *bufio.Reader) ([]byte, error) {
	allCon, err := r.ReadBytes('\n')
	if err != nil {
		return allCon, err
	}
	if bytes.HasSuffix(allCon, []byte("\r\n")) {
		return allCon, nil
	}
	for {
		con, err := r.ReadBytes('\n')
		if err != nil {
			return allCon, err
		}
		allCon = append(allCon, con...)
		if bytes.HasSuffix(allCon, []byte("\r\n")) {
			return allCon, nil
		}
	}
}
func readCRLFWithString(r *bufio.Reader) (string, error) {
	con, err := readCRLFWithBytes(r)
	allCon := tools.BytesToString(con)
	return allCon, err
}
func readHeadersWithKv(line string) (string, string, error) {
	kvs := strings.SplitN(line, ":", 2)
	if len(kvs) != 2 {
		return "", "", errors.New("invalid header line: " + line)
	}
	return strings.TrimSpace(kvs[0]), strings.TrimSpace(kvs[1]), nil
}

func readHeaders(r *bufio.Reader) (http.Header, error) {
	headers := make(http.Header)
	for {
		line, err := readCRLFWithString(r)
		if err != nil {
			return headers, err
		}
		if line == "\r\n" {
			return headers, nil
		}
		key, val, err := readHeadersWithKv(strings.TrimRight(line, "\r\n"))
		if err != nil {
			return headers, err
		}
		headers.Add(key, val)
	}
}
func writeHeaders(headers http.Header, w io.Writer) (err error) {
	for key, vals := range headers {
		for _, val := range vals {
			if _, err = fmt.Fprintf(w, "%s: %s\r\n", key, val); err != nil {
				return
			}
		}
	}
	return nil
}
