package http1

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"

	"github.com/gospider007/tools"
)

func ReadResponse(r *bufio.Reader) (*http.Response, error) {
	line, err := readCRLFWithString(r)
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")

	proto, status, ok := strings.Cut(line, " ")
	if !ok {
		return nil, errors.New("malformed HTTP response")
	}
	resp := &http.Response{
		Proto:         proto,
		Status:        strings.TrimLeft(status, " "),
		ContentLength: -1,
	}
	statusCode, _, _ := strings.Cut(resp.Status, " ")
	if resp.StatusCode, err = strconv.Atoi(statusCode); err != nil {
		return nil, errors.New("malformed HTTP status code")
	}
	if resp.ProtoMajor, resp.ProtoMinor, ok = http.ParseHTTPVersion(resp.Proto); !ok {
		return nil, errors.New("malformed HTTP version")
	}
	resp.Header, err = readHeaders(r)
	if err != nil {
		return nil, err
	}
	if contentLenStr := resp.Header.Get("Content-Length"); contentLenStr != "" {
		contentLength, err := strconv.Atoi(contentLenStr)
		if err != nil {
			return resp, err
		}
		resp.ContentLength = int64(contentLength)
	}
	if resp.StatusCode == 101 {
		resp.Close = true
	}
	switch {
	case resp.Header.Get("Transfer-Encoding") == "chunked":
		resp.Body = newChunkedReader(r)
	case resp.ContentLength == 0:
		resp.Body = http.NoBody
	case resp.ContentLength > 0:
		resp.Body = io.NopCloser(io.LimitReader(r, resp.ContentLength))
	case resp.Header.Get("Connection") == "close":
		resp.Body = io.NopCloser(r)
		resp.Close = true
	default:
		resp.Body = http.NoBody
	}
	return resp, nil
}

func WriteResponse(resp *http.Response, w io.Writer, encoding string) (err error) {
	var arch tools.Compression
	respEncoding := resp.Header.Get("Content-Encoding")
	if respEncoding == "" && encoding != "" {
		arch, err = tools.NewRawCompression(encoding)
		if err != nil {
			return err
		}
		resp.ContentLength = -1
		resp.Header.Set("Content-Encoding", encoding)
	}
	text := resp.Status
	if text == "" {
		text = http.StatusText(resp.StatusCode)
		if text == "" {
			text = "status code " + strconv.Itoa(resp.StatusCode)
		}
	} else {
		text = strings.TrimPrefix(text, strconv.Itoa(resp.StatusCode)+" ")
	}
	if _, err := fmt.Fprintf(w, "HTTP/%d.%d %03d %s\r\n", 1, 1, resp.StatusCode, text); err != nil {
		return err
	}
	if resp.ContentLength == -1 {
		resp.Header.Set("Transfer-Encoding", "chunked")
		resp.Header.Del("Content-Length")
	}

	if err = writeHeaders(resp.Header, w); err != nil {
		return
	}
	if _, err := io.WriteString(w, "\r\n"); err != nil {
		return err
	}
	if resp.ContentLength == -1 {
		var chunkR io.Reader
		if arch != nil {
			pr, pw := io.Pipe()
			chunkR = pr
			archWriter, err := arch.OpenWriter(pw)
			if err != nil {
				return err
			}
			go func() {
				_, cerr := io.Copy(archWriter, resp.Body)
				pw.CloseWithError(cerr)
				archWriter.Close()
			}()
		} else {
			chunkR = resp.Body
		}
		chunkW := httputil.NewChunkedWriter(w)
		if _, err = tools.Copy(chunkW, chunkR); err != nil {
			return err
		}
		if err = chunkW.Close(); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return err
		}
	} else {
		if _, err = tools.Copy(w, resp.Body); err != nil {
			return err
		}
	}
	return resp.Body.Close()
}
