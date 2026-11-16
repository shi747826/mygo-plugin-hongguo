package pluginapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type Request struct {
	Version int             `json:"version"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Cancel  uint64          `json:"cancel,omitempty"`
}
type Response struct {
	Version int             `json:"version"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Serve multiplexes bounded JSONL calls. Standard output must be reserved for this protocol.
// Ping is handled here and returns ProtocolVersion. Handlers must honor context cancellation.
func Serve(in io.Reader, out io.Writer, handler func(context.Context, string, json.RawMessage) (any, error)) error {
	ctx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()
	var mu, writeMu sync.Mutex
	pending := map[uint64]context.CancelFunc{}
	var wg sync.WaitGroup
	defer func() { cancelAll(); wg.Wait() }()
	var writeErr error
	send := func(r Response) error {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if len(raw) >= MaxMessageSize {
			raw, _ = json.Marshal(Response{Version: ProtocolVersion, ID: r.ID, Error: EncodeError(fmt.Errorf("%w: response too large", ErrProtocol))})
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if writeErr != nil {
			return writeErr
		}
		_, writeErr = out.Write(append(raw, '\n'))
		return writeErr
	}
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), MaxMessageSize)
	for scan.Scan() {
		var r Request
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil || r.Version != ProtocolVersion {
			return ErrProtocol
		}
		if r.Cancel != 0 {
			mu.Lock()
			f := pending[r.Cancel]
			mu.Unlock()
			if f != nil {
				f()
			}
			continue
		}
		if r.ID == 0 || r.Method == "" {
			return ErrProtocol
		}
		mu.Lock()
		if _, exists := pending[r.ID]; exists {
			mu.Unlock()
			return ErrProtocol
		}
		if len(pending) >= 128 {
			mu.Unlock()
			if err := send(Response{Version: ProtocolVersion, ID: r.ID, Error: EncodeError(fmt.Errorf("%w: too many requests", ErrProtocol))}); err != nil {
				return err
			}
			continue
		}
		callCtx, cancel := context.WithCancel(ctx)
		pending[r.ID] = cancel
		mu.Unlock()
		wg.Add(1)
		go func(r Request) {
			defer wg.Done()
			defer cancel()
			var value any
			var err error
			if r.Method == "Ping" {
				value = ProtocolVersion
			} else {
				value, err = handler(callCtx, r.Method, r.Params)
			}
			raw, merr := json.Marshal(value)
			if merr != nil {
				err = merr
			}
			if e := send(Response{Version: ProtocolVersion, ID: r.ID, Result: raw, Error: EncodeError(err)}); e != nil {
				cancelAll()
			}
			mu.Lock()
			delete(pending, r.ID)
			mu.Unlock()
		}(r)
	}
	if err := scan.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return nil
}
