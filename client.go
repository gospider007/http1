package http1

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/gospider007/tools"
)

type rsp struct {
	r   *http.Response
	err error
}

type conn struct {
	conn        net.Conn
	r           *bufio.Reader
	w           *bufio.Writer
	ctx         context.Context
	cnl         context.CancelCauseFunc
	rsps        chan *rsp
	bodyContext context.Context
}

func (obj *conn) SetBodyContext(ctx context.Context) {
	obj.bodyContext = ctx
}
func (obj *conn) BodyContext() context.Context {
	return obj.bodyContext
}
func NewConn(preCtx context.Context, con net.Conn) *conn {
	ctx, cnl := context.WithCancelCause(preCtx)
	c := &conn{
		ctx:  ctx,
		cnl:  cnl,
		conn: con,
		rsps: make(chan *rsp),
		r:    bufio.NewReader(con),
		w:    bufio.NewWriter(con),
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
		res, err := ReadResponse(obj.r)
		if err != nil {
			return tools.WrapError(err, "read response failed")
		}
		if res.Body == nil {
			res.Body = http.NoBody
		}
		var r *rsp
		if res.Body == http.NoBody || res.Close {
			if res.StatusCode == 101 {
				res.Body = NewBody(res.Body, obj, nil, nil, false, nil)
			} else {
				res.Body = NewBody(res.Body, obj, nil, nil, res.Close, nil)
			}
			r = &rsp{r: res, err: err}
			select {
			case obj.rsps <- r:
			case <-obj.ctx.Done():
				return errors.New("client closed")
			}
			if res.Close {
				return nil
			}
		} else {
			ctx, cnl := context.WithCancelCause(obj.ctx)
			res.Body = NewBody(res.Body, obj, ctx, cnl, res.Close, nil)
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
	err = WriteRequest(req, obj.w, rawHeaders, orderHeaders)

	if err != nil {
		obj.CloseWithError(tools.WrapError(err, "failed to send request body"))
	}
	return
}

type Conn interface {
	Context() context.Context
	CloseWithError(err error) error
	DoRequest(context.Context, *http.Request, *Option) (*http.Response, error)
	Stream() io.ReadWriteCloser
}

type Body struct {
	writeDone chan struct{}
	close     bool
	r         io.ReadCloser
	c         Conn
	ctx       context.Context
	cnl       context.CancelCauseFunc
}

func NewBody(r io.ReadCloser, c Conn, ctx context.Context, cnl context.CancelCauseFunc, close bool, writeDone chan struct{}) *Body {
	return &Body{
		close:     close,
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
	if obj.close {
		obj.c.CloseWithError(errors.New("body connection closed"))
	}
	return nil
}

func (obj *conn) DoRequest(ctx context.Context, req *http.Request, option *Option) (res *http.Response, err error) {
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
		case rsp := <-obj.rsps:
			return rsp.r, rsp.err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-obj.ctx.Done():
			return nil, context.Cause(obj.ctx)
		}
	case rsp := <-obj.rsps:
		if rsp.err == nil {
			rsp.r.Body.(*Body).SetWriteDone(writeDone)
		}
		return rsp.r, rsp.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-obj.ctx.Done():
		return nil, context.Cause(obj.ctx)
	}
}

func (obj *conn) Close() error {
	return obj.CloseWithError(nil)
}
func (obj *conn) Context() context.Context {
	return obj.ctx
}
func (obj *conn) CloseWithError(err error) error {
	obj.cnl(err)
	return obj.conn.Close()
}

func (obj *conn) Stream() io.ReadWriteCloser {
	return &websocketConn{
		con: obj,
	}
}

type websocketConn struct {
	con *conn
}

func (obj *websocketConn) Read(p []byte) (n int, err error) {
	return obj.con.r.Read(p)
}
func (obj *websocketConn) Write(p []byte) (n int, err error) {
	n, err = obj.con.w.Write(p)
	if err != nil {
		return
	}
	err = obj.con.w.Flush()
	return
}

func (obj *websocketConn) Close() error {
	return obj.con.CloseWithError(nil)
}
