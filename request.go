package http1

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gospider007/tools"
	"golang.org/x/net/http/httpguts"
)

func WriteRequest(req *http.Request, w *bufio.Writer, rawHeaders http.Header, orderHeaders []interface {
	Key() string
	Val() any
}) (err error) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	host, err = httpguts.PunycodeHostPort(host)
	if err != nil {
		return
	}
	host = removeZone(host)
	if rawHeaders.Get("Host") == "" {
		rawHeaders.Set("Host", host)
	}
	contentL, chunked := tools.GetContentLength(req)
	if contentL >= 0 {
		rawHeaders.Set("Content-Length", fmt.Sprint(contentL))
	} else if chunked {
		rawHeaders.Set("Transfer-Encoding", "chunked")
	}
	ruri := req.URL.RequestURI()
	if req.Method == "CONNECT" && req.URL.Path == "" {
		if req.URL.Opaque != "" {
			ruri = req.URL.Opaque
		} else {
			ruri = host
		}
	}
	if _, err = fmt.Fprintf(w, "%s %s %s\r\n", req.Method, ruri, req.Proto); err != nil {
		return
	}
	//write headers
	for _, kv := range tools.NewHeadersWithH1(orderHeaders, rawHeaders) {
		if _, err = fmt.Fprintf(w, "%s: %s\r\n", kv[0], kv[1]); err != nil {
			return
		}
	}
	//write headers end
	if _, err = w.WriteString("\r\n"); err != nil {
		return
	}
	if req.Body == nil {
		err = w.Flush()
		return
	}
	if chunked {
		chunkedWriter := newChunkedWriter(w)
		if _, err = tools.Copy(chunkedWriter, req.Body); err != nil {
			return
		}
		if err = chunkedWriter.Close(); err != nil {
			return
		}
	} else {
		if _, err = tools.Copy(w, req.Body); err != nil {
			return
		}
	}
	err = w.Flush()
	return
}
func ReadRequest(r *bufio.Reader) (req *http.Request, err error) {
	line, err := readCRLFWithString(r)
	if err != nil {
		return
	}
	line = strings.TrimRight(line, "\r\n")

	method, rest, ok := strings.Cut(line, " ")
	if !ok {
		return nil, fmt.Errorf("malformed HTTP request line: %q", line)
	}
	requestURI, proto, ok := strings.Cut(rest, " ")
	if !ok {
		return nil, fmt.Errorf("malformed HTTP request line: %q", line)
	}
	req = &http.Request{
		Method:        method,
		RequestURI:    requestURI,
		Proto:         proto,
		ContentLength: -1,
	}
	if req.ProtoMajor, req.ProtoMinor, ok = http.ParseHTTPVersion(req.Proto); !ok {
		return nil, fmt.Errorf("malformed HTTP version in HTTP request line")
	}
	rawurl := req.RequestURI
	if req.Method == "CONNECT" && !strings.HasPrefix(rawurl, "/") {
		rawurl = "http://" + rawurl
	}
	if req.URL, err = url.ParseRequestURI(rawurl); err != nil {
		return nil, err
	}
	req.Header, err = readHeaders(r)
	if err != nil {
		return nil, err
	}
	req.Host = req.URL.Host
	if req.Host == "" {
		req.Host = req.Header.Get("Host")
	}

	if contentLenStr := req.Header.Get("Content-Length"); contentLenStr != "" {
		contentLength, err := strconv.Atoi(contentLenStr)
		if err != nil {
			return req, err
		}
		req.ContentLength = int64(contentLength)
	}
	switch {
	case req.Header.Get("Transfer-Encoding") == "chunked":
		req.Body = newChunkedReader(r)
	case req.ContentLength == 0:
		req.Body = http.NoBody
	case req.ContentLength > 0:
		req.Body = io.NopCloser(io.LimitReader(r, req.ContentLength))
	default:
		req.Body = http.NoBody
	}
	return req, nil
}
