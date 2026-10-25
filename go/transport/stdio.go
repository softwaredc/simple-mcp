package transport

import (
	"bufio"
	"bytes"
	"io"
)

// MaxLineBytes caps one JSON-RPC message on stdio. The default would be
// unbounded, which is a denial-of-service vector when a client can push
// arbitrarily large tool results; 16 MiB is far above any real MCP message
// and still bounds memory per connection.
const MaxLineBytes = 16 << 20

// StdioTransport speaks MCP over any reader/writer pair. In production that is
// os.Stdin/os.Stdout; in tests it is an io.Pipe.
//
// Two things here look trivial and are not:
//
//  1. A hand-rolled line reader over ReadSlice, not bufio.Scanner and not
//     ReadBytes. Scanner caps a token at 64 KiB by default and reports
//     bufio.ErrTooLong beyond that, so a tool returning a 100 KiB document
//     fails with a confusing "token too long" that has nothing to do with
//     MCP. ReadBytes fixes that but grows without bound first, which turns a
//     line-length limit into a size check that happens too late to prevent
//     the allocation. readLine below applies the limit while reading.
//
//  2. Every write ends with exactly one '\n' and is flushed. The stdio
//     transport is line-delimited: a peer blocked in ReadBytes will wait
//     forever for a newline that never arrives if a write is buffered.
type StdioTransport struct {
	r         *bufio.Reader
	w         io.Writer
	closer    io.Closer
	lineLimit int
}

// NewStdio builds a transport over r and w. closer may be nil.
func NewStdio(r io.Reader, w io.Writer, closer io.Closer) *StdioTransport {
	return &StdioTransport{
		r:         bufio.NewReaderSize(r, 64<<10),
		w:         w,
		closer:    closer,
		lineLimit: MaxLineBytes,
	}
}

// ReadMessage reads one newline-delimited JSON message.
//
// Blank lines are skipped rather than treated as parse errors: the stdio
// framing is a stream of lines, and stray whitespace between messages is
// harmless. A genuine parse failure surfaces in the protocol layer, which
// knows enough to answer with -32700.
func (t *StdioTransport) ReadMessage() ([]byte, error) {
	for {
		line, err := t.readLine()
		if err != nil {
			// A final line without a newline is still a complete message if
			// the peer closed the write half after sending it.
			if err == io.EOF && len(bytes.TrimSpace(line)) > 0 {
				return trimLine(line), nil
			}
			return nil, err
		}
		trimmed := trimLine(line)
		if len(trimmed) == 0 {
			continue
		}
		return trimmed, nil
	}
}

// readLine reads one '\n'-terminated line while enforcing lineLimit as an
// allocation bound, not just a size check after the fact.
//
// The first version of this used ReadBytes and compared its result against
// lineLimit. That check arrives too late: ReadBytes keeps growing its result
// until it finds the newline, so a peer that sends gigabytes with no newline
// makes us allocate all of it before we decide to reject the message. The
// limit has to be applied while reading, which means collecting the line in
// fragments ourselves.
//
// ReadSlice gives us the hook for that: it returns the bytes buffered so far
// and reports ErrBufferFull when the line is still going. Note that each
// fragment points into the reader's own buffer, so it must be copied out
// before the next call — hence append rather than retaining the slice.
func (t *StdioTransport) readLine() ([]byte, error) {
	var line []byte
	for {
		frag, err := t.r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// No newline yet. Check before accumulating, or the check buys
			// nothing: an unbounded number of bounded fragments is still
			// unbounded memory.
			if len(line)+len(frag) > t.lineLimit {
				return nil, ErrLineTooLong
			}
			line = append(line, frag...)
			continue
		}

		// err is nil (complete line), io.EOF (maybe a final partial line),
		// or a genuine read failure. Either way these are the last bytes.
		if len(line)+len(frag) > t.lineLimit {
			return nil, ErrLineTooLong
		}
		line = append(line, frag...)
		return line, err
	}
}

func trimLine(line []byte) []byte {
	out := bytes.TrimRight(line, "\r\n")
	return bytes.TrimSpace(out)
}

// WriteMessage writes one message followed by a newline.
func (t *StdioTransport) WriteMessage(msg []byte) error {
	if len(msg) == 0 {
		return nil
	}
	buf := make([]byte, 0, len(msg)+1)
	buf = append(buf, msg...)
	buf = append(buf, '\n')
	// os.Stdout.Write usually consumes everything, but io.Writer only promises
	// that it will consume some prefix before returning nil. Without this loop
	// a short write emits half a line, and the peer blocked in readLine waits
	// forever for a newline that never comes.
	for len(buf) > 0 {
		n, werr := t.w.Write(buf)
		if n > 0 {
			buf = buf[n:]
		}
		if werr != nil {
			return werr
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	if f, ok := t.w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// Close closes the closer if one was supplied.
func (t *StdioTransport) Close() error {
	if t.closer != nil {
		return t.closer.Close()
	}
	return nil
}
