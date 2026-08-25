package exescroll

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestClientProtocol(t *testing.T) {
	server, clientConn := net.Pipe()
	exitFile := filepath.Join(t.TempDir(), "exit")
	client := &Client{conn: clientConn, exitFile: exitFile}
	defer clientConn.Close()

	done := make(chan error, 1)
	go func() {
		defer server.Close()
		typ, payload, err := readFrame(server)
		if err != nil {
			done <- err
			return
		}
		if typ != messageData || !bytes.Equal(payload, []byte("hello")) {
			done <- io.ErrUnexpectedEOF
			return
		}
		var header [5]byte
		header[0] = messageData
		binary.LittleEndian.PutUint32(header[1:], 5)
		if err := writeAll(server, append(header[:], []byte("world")...)); err != nil {
			done <- err
			return
		}
		if err := os.WriteFile(exitFile, []byte("42\n"), 0o600); err != nil {
			done <- err
			return
		}
		done <- nil
	}()

	if err := client.SendInput([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	msg, err := client.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Kind != MessageOutput || string(msg.Data) != "world" {
		t.Fatalf("output message = %+v", msg)
	}
	msg, err = client.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Kind != MessageExit || msg.ExitCode != 42 {
		t.Fatalf("exit message = %+v", msg)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestResizePayloadUsesWinsizeLayout(t *testing.T) {
	server, clientConn := net.Pipe()
	client := &Client{conn: clientConn}
	defer clientConn.Close()

	done := make(chan []byte, 1)
	go func() {
		defer server.Close()
		typ, payload, err := readFrame(server)
		if err != nil || typ != messageWinch {
			done <- nil
			return
		}
		done <- payload
	}()
	if err := client.SendResize(120, 40); err != nil {
		t.Fatal(err)
	}
	payload := <-done
	if len(payload) != 8 || binary.LittleEndian.Uint16(payload[0:2]) != 40 || binary.LittleEndian.Uint16(payload[2:4]) != 120 {
		t.Fatalf("winsize payload = %v", payload)
	}
}
