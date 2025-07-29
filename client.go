package http1

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/gospider007/tools"
	"golang.org/x/net/http/httpguts"
)

type rsp struct {
	r   *http.Response
	err error
}

type conn struct {
	conn      net.Conn
	r         *bufio.Reader
	w         *bufio.Writer
	closeFunc func(error)
	ctx       context.Context
	cnl       context.CancelCauseFunc
	rsps      chan *rsp
}

func NewClientConn(preCtx context.Context, con net.Conn, closeFunc func(error)) *conn {
	ctx, cnl := context.WithCancelCause(preCtx)
	c := &conn{
		ctx:       ctx,
		cnl:       cnl,
		conn:      con,
		closeFunc: closeFunc,
		rsps:      make(chan *rsp),
		r:         bufio.NewReader(con),
		w:         bufio.NewWriter(con),
	}
	go c.read()
	return c
}
func (obj *conn) read() (err error) {
	defer func() {
		if err != nil && err != tools.ErrNoErr {
			obj.CloseWithError(err)
		}
	}()
	for {
		res, err := http.ReadResponse(obj.r, nil)
		if err != nil {
			return err
		}
		if res.Body == nil {
			res.Body = http.NoBody
		}
		var r *rsp
		if res.Body == http.NoBody || res.StatusCode == 101 {
			res.Body = NewBody(res.Body, obj, nil, nil, nil)
			r = &rsp{r: res, err: err}
			select {
			case obj.rsps <- r:
			case <-obj.ctx.Done():
				return errors.New("client closed")
			}
			if res.StatusCode == 101 {
				return nil
			}
		} else {
			ctx, cnl := context.WithCancelCause(obj.ctx)
			res.Body = NewBody(res.Body, obj, ctx, cnl, nil)
			r = &rsp{r: res, err: err}
			select {
			case obj.rsps <- r:
			case <-obj.ctx.Done():
				return errors.New("client closed")
			}
			<-ctx.Done()
		}
		select {
		case <-obj.ctx.Done():
			return errors.New("client closed")
		default:
		}
	}
}
func removeZone(host string) string {
	if !strings.HasPrefix(host, "[") {
		return host
	}
	i := strings.LastIndex(host, "]")
	if i < 0 {
		return host
	}
	j := strings.LastIndex(host[:i], "%")
	if j < 0 {
		return host
	}
	return host[:j] + host[i:]
}

func (obj *conn) httpWrite(req *http.Request, rawHeaders http.Header, orderHeaders []interface {
	Key() string
	Val() any
}) (err error) {
	err = HttpWrite(req, obj.w, rawHeaders, orderHeaders)

	if err != nil {
		obj.CloseWithError(tools.WrapError(err, "failed to send request body"))
	}
	return
}
func HttpWrite(req *http.Request, w *bufio.Writer, rawHeaders http.Header, orderHeaders []interface {
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
	for _, kv := range tools.NewHeadersWithH1(orderHeaders, rawHeaders) {
		if _, err = fmt.Fprintf(w, "%s: %s\r\n", kv[0], kv[1]); err != nil {
			return
		}
	}
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

type Conn interface {
	Context() context.Context
	CloseWithError(err error) error
	DoRequest(*http.Request, *Option) (*http.Response, error)
	Stream() io.ReadWriteCloser
}

type Body struct {
	writeDone chan struct{}
	r         io.ReadCloser
	c         Conn
	ctx       context.Context
	cnl       context.CancelCauseFunc
}

func NewBody(r io.ReadCloser, c Conn, ctx context.Context, cnl context.CancelCauseFunc, writeDone chan struct{}) *Body {
	return &Body{
		r:         r,
		c:         c,
		ctx:       ctx,
		cnl:       cnl,
		writeDone: writeDone,
	}
}

func (obj *Body) SetWriteDone(writeDone chan struct{}) {
	obj.writeDone = writeDone
}

func (obj *Body) Read(p []byte) (n int, err error) {
	return obj.r.Read(p)
}
func (obj *Body) Close() error {
	return obj.CloseWithError(nil)
}
func (obj *Body) Stream() io.ReadWriteCloser {
	return obj.c.Stream()
}
func (obj *Body) Context() context.Context {
	return obj.ctx
}
func (obj *Body) CloseWithError(err error) error {
	if err != nil && err != tools.ErrNoErr {
		obj.c.CloseWithError(errors.New("write not done"))
	} else if obj.writeDone != nil {
		select {
		case <-obj.writeDone:
		default:
			obj.c.CloseWithError(errors.New("write not done"))
		}
	}
	obj.r.Close()
	if obj.cnl != nil {
		obj.cnl(err)
	}
	return nil
}

func (obj *conn) DoRequest(req *http.Request, option *Option) (res *http.Response, err error) {
	defer func() {
		if err != nil {
			obj.CloseWithError(tools.WrapError(err, "failed to send request"))
		}
	}()
	var writeErr error
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		writeErr = obj.httpWrite(req, req.Header.Clone(), option.OrderHeaders)
		if writeErr != nil {
			obj.CloseWithError(tools.WrapError(writeErr, "failed to httpWrite"))
		}
	}()
	select {
	case <-writeDone:
		if writeErr != nil {
			return nil, writeErr
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-obj.ctx.Done():
			return nil, context.Cause(obj.ctx)
		case rsp := <-obj.rsps:
			return rsp.r, rsp.err
		}
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-obj.ctx.Done():
		return nil, context.Cause(obj.ctx)
	case rsp := <-obj.rsps:
		if rsp.err == nil {
			rsp.r.Body.(*Body).SetWriteDone(writeDone)
		}
		return rsp.r, rsp.err
	}
}

func (obj *conn) Close() error {
	return obj.CloseWithError(nil)
}
func (obj *conn) Context() context.Context {
	return obj.ctx
}
func (obj *conn) CloseWithError(err error) error {
	if obj.closeFunc != nil {
		obj.closeFunc(err)
	}
	obj.cnl(err)
	return obj.conn.Close()
}

func (obj *conn) Stream() io.ReadWriteCloser {
	return &websocketConn{
		cnl: obj.cnl,
		r:   obj.r,
		w:   obj.conn,
	}
}

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

type websocketConn struct {
	r   io.Reader
	w   io.WriteCloser
	cnl context.CancelCauseFunc
}

func (obj *websocketConn) Read(p []byte) (n int, err error) {
	return obj.r.Read(p)
}
func (obj *websocketConn) Write(p []byte) (n int, err error) {
	return obj.w.Write(p)
}
func (obj *websocketConn) Close() error {
	obj.cnl(nil)
	return obj.w.Close()
}
