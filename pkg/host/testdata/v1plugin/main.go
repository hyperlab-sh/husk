// Command v1plugin is a fake major-1 plugin for the host's end-to-end tests.
// It never writes stdout. Usage: v1plugin <out-file> <claimed-plugin-id>.
// It connects to $HUSK_SOCKET_PATH, sends a v1 hello claiming the id, writes
// the first line the host answers with to out-file, then blocks until its
// stdin closes or it is terminated. An out-file of "-" makes it idle only.
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: v1plugin <out-file> <claimed-plugin-id>")
		os.Exit(2)
	}
	out, id := os.Args[1], os.Args[2]
	// Caught so the host's stop cannot kill the process before it recorded the
	// answer; it exits after writing.
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	if out != "-" {
		answer, err := hello(os.Getenv("HUSK_SOCKET_PATH"), id)
		if err != nil {
			answer = "ERROR: " + err.Error() + "\n"
		}
		tmp := out + ".tmp"
		if err := os.WriteFile(tmp, []byte(answer), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.Rename(tmp, out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	closed := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(closed) }()
	select {
	case <-closed:
	case <-term:
	}
}

// hello sends the v1 envelope and returns the first line of the reply.
func hello(socket, id string) (string, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "{\"type\":\"hello\",\"hello\":{\"plugin_id\":%q,\"name\":\"fake\",\"protocol_version\":\"1.0\"}}\n", id); err != nil {
		return "", err
	}
	return bufio.NewReader(conn).ReadString('\n')
}
