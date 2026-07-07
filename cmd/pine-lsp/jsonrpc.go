package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// message is a single JSON-RPC 2.0 frame. The same struct serves requests,
// responses and notifications: an id present + method set is a request; id
// present + result/error set is a response; method set + no id is a
// notification.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the error object of a JSON-RPC response.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// conn is a bidirectional LSP connection: it reads/writes JSON-RPC messages
// framed with the LSP `Content-Length` header over any reader/writer pair
// (stdin/stdout in production, an io.Pipe in tests). Writes are serialized so
// server-initiated notifications (publishDiagnostics) never interleave with
// request responses.
type conn struct {
	r  *bufio.Reader
	w  io.Writer
	mu sync.Mutex
}

func newConn(r io.Reader, w io.Writer) *conn {
	return &conn{r: bufio.NewReader(r), w: w}
}

// read blocks for the next message, decoding the Content-Length framing. It
// returns io.EOF when the peer closes the stream.
func (c *conn) read() (*message, error) {
	length := -1
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // blank line terminates the header block
			break
		}
		if i := strings.IndexByte(line, ':'); i >= 0 {
			name := strings.TrimSpace(line[:i])
			val := strings.TrimSpace(line[i+1:])
			if strings.EqualFold(name, "Content-Length") {
				n, err := strconv.Atoi(val)
				if err != nil {
					return nil, fmt.Errorf("bad Content-Length %q: %w", val, err)
				}
				length = n
			}
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, fmt.Errorf("decode body: %w", err)
	}
	return &m, nil
}

// write frames and sends one message with the mandatory Content-Length header.
func (c *conn) write(m *message) error {
	m.JSONRPC = "2.0"
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.w.Write(body)
	return err
}

// respond replies to a request id with a result (marshaled to JSON).
func (c *conn) respond(id json.RawMessage, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return c.write(&message{ID: id, Result: raw})
}

// respondErr replies to a request id with a JSON-RPC error.
func (c *conn) respondErr(id json.RawMessage, code int, msg string) error {
	return c.write(&message{ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// notify sends a server-initiated notification (no id).
func (c *conn) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(&message{Method: method, Params: raw})
}
