package transport

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The headline test in this file: a message far larger than bufio.Scanner's
// default 64 KiB token limit must cross the transport intact.
//
// This is not hypothetical. A tool that returns a source file, a JSON dump, or
// a base64 image blows past 64 KiB routinely. An implementation built on
// bufio.Scanner fails with bufio.ErrTooLong and an error message that mentions
// nothing about MCP, which sends people hunting in the wrong place for hours.
func TestLargeMessageSurvivesStdio(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 300*1024) // 300 KiB
	msg := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"big","arguments":{"payload":"` +
		string(big) + `"}}}`)

	var out bytes.Buffer
	tr := NewStdio(strings.NewReader(string(msg)+"\n"), &out, nil)

	got, err := tr.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("message corrupted: got %d bytes, want %d", len(got), len(msg))
	}
}

func TestScannerWouldHaveFailedHere(t *testing.T) {
	// Documents the trap this package avoids, and asserts it rather than
	// printing it: an earlier draft of this test only logged the outcome on
	// both branches, so it passed whether or not Scanner had the limit we
	// claim it has. A test that cannot fail is worse than no test, because it
	// looks like coverage.
	big := bytes.Repeat([]byte("x"), 300*1024)
	sc := newTestScanner(strings.NewReader(string(big) + "\n"))
	if sc.Scan() {
		t.Fatal("scanner accepted 300 KiB: the default buffer changed, and the rationale in transport.go no longer holds")
	}
	if !errors.Is(sc.Err(), bufio.ErrTooLong) {
		t.Fatalf("scanner error = %v, want bufio.ErrTooLong", sc.Err())
	}
}

// TestLineLimitIsEnforcedWhileReading pins down a limit that is easy to write
// and easy to get wrong.
//
// Comparing the length of a ReadBytes result against MaxLineBytes satisfies
// intuition and nothing else: ReadBytes accumulates the whole line before it
// returns, so the memory is spent well before the verdict. Both cases here
// therefore use input with no newline at all, which is what a hostile peer
// sends and where checking afterwards costs the most.
func TestLineLimitIsEnforcedWhileReading(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		// Fits in the 64 KiB read buffer: ReadSlice returns it in one call.
		{"fits in one buffer", strings.Repeat("x", 200)},
		// Spans several: ErrBufferFull, per-fragment accumulation. A limit
		// applied only at the end would have grown unbounded by now.
		{"spans many buffers", strings.Repeat("x", 300*1024)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewStdio(strings.NewReader(tc.input), io.Discard, nil)
			tr.lineLimit = 64 // the real limit is 16 MiB; this makes the test cheap
			if _, err := tr.ReadMessage(); !errors.Is(err, ErrLineTooLong) {
				t.Fatalf("err = %v, want ErrLineTooLong", err)
			}
		})
	}
}

// TestLineLimitStopsReadingBeforeEOF is the one that actually distinguishes a
// real limit from a cosmetic one.
//
// The peer here writes well past the limit and never sends a newline, never
// closes, never delivers EOF. An implementation that reads the whole line
// first and checks its length afterwards cannot answer this until EOF arrives,
// so it hangs while buffering everything the peer chooses to send. The
// requirement is not "eventually refuse", it is "refuse without reading on".
func TestLineLimitStopsReadingBeforeEOF(t *testing.T) {
	pr, pw := io.Pipe()
	tr := NewStdio(pr, io.Discard, nil)
	tr.lineLimit = 64 // the real limit is 16 MiB; this keeps the test cheap

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); pr.Close() })

	go func() {
		defer pw.Close()
		chunk := strings.Repeat("x", 4096)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := pw.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}()

	errc := make(chan error, 1)
	go func() {
		_, err := tr.ReadMessage()
		errc <- err
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("err = %v, want ErrLineTooLong", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("transport read past the limit waiting for EOF: " +
			"a peer that never sends a newline can make it allocate without bound")
	}
}

// TestHTTPCloseDuringWriteDoesNotPanic covers the race the obvious version of
// this transport has in hiding: waking the reader by sending on a channel that
// Close also closes.
//
// Closing wake is the natural way to unblock a reader, and it is a bug here.
// Close cannot know whether some goroutine sits between having released the
// mutex and having issued its send, so it sometimes wins that race and panics
// with "send on closed channel". Run this under -race, as CI does.
func TestHTTPCloseDuringWriteDoesNotPanic(t *testing.T) {
	srv := newTestHTTPServer(t)
	defer srv.close()

	tr := NewHTTP(srv.url, srv.client())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tr.WriteMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = tr.Close()
	}()
	wg.Wait()

	// Whatever order those ran in, the reader must reach EOF rather than block.
	// A panic fails the test process on its own, so surviving is half the
	// assertion; this deadline is the other half.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := tr.ReadMessage(); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadMessage blocked forever after Close")
	}
}

func TestBlankLinesSkipped(t *testing.T) {
	in := "\n\n" + `{"a":1}` + "\n\n\n" + `{"b":2}` + "\n"
	tr := NewStdio(strings.NewReader(in), io.Discard, nil)

	first, err := tr.ReadMessage()
	if err != nil || string(first) != `{"a":1}` {
		t.Fatalf("first = %q err = %v", first, err)
	}
	second, err := tr.ReadMessage()
	if err != nil || string(second) != `{"b":2}` {
		t.Fatalf("second = %q err = %v", second, err)
	}
}

func TestWriteAppendsNewline(t *testing.T) {
	var out bytes.Buffer
	tr := NewStdio(strings.NewReader(""), &out, nil)
	if err := tr.WriteMessage([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"a\":1}\n" {
		t.Fatalf("write must add exactly one newline, got %q", out.String())
	}
}

func TestFinalLineWithoutNewline(t *testing.T) {
	// A peer that writes a message and closes without a trailing newline is
	// still a complete message. Treating it as EOF loses the last reply.
	tr := NewStdio(strings.NewReader(`{"a":1}`), io.Discard, nil)
	got, err := tr.ReadMessage()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestHTTPTransportRoundTrip(t *testing.T) {
	// A real HTTP server, a real client transport, one request and one
	// notification, checked end to end.
	srv := newTestHTTPServer(t)
	defer srv.close()

	tr := NewHTTP(srv.url, srv.client())
	defer tr.Close()

	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if err := tr.WriteMessage(req); err != nil {
		t.Fatal(err)
	}
	got, err := tr.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"id":1`)) {
		t.Fatalf("unexpected response: %s", got)
	}

	// A notification gets 202 and no body, so nothing is queued and the next
	// ReadMessage would block. That is correct behaviour, and this is where a
	// naive implementation deadlocks.
	note := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if err := tr.WriteMessage(note); err != nil {
		t.Fatalf("notification write: %v", err)
	}
}

func TestHTTPTransportSSE(t *testing.T) {
	srv := newTestSSEServer(t)
	defer srv.close()

	tr := NewHTTP(srv.url, srv.client())
	defer tr.Close()

	if err := tr.WriteMessage([]byte(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := tr.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"id":7`)) {
		t.Fatalf("SSE response not parsed: %s", got)
	}
}
