package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestByteExactPipeliningAndManagementWarning(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	records := make(chan fakeherdr.Request, 16)
	fake, err := fakeherdr.Start(filepath.Join(dir, "u.sock"), fakeherdr.Options{Passthrough: []string{"vendor.unknown"}, OnRequest: func(req fakeherdr.Request) { records <- req }})
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	warnings := make(chan string, 4)
	s, err := Start(context.Background(), Options{Dial: upstream.New(upstream.Options{Socket: filepath.Join(dir, "u.sock")}).Dial, Warn: func(m string) { warnings <- m }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	lines := []string{"{ \"method\" : \"vendor.unknown\", \"id\" : \"opaque\", \"params\":{\"extra\":[1,true]} }\r\n", "{\"id\":\"manage\", \"method\":\"server.stop\", \"params\":{}}\n"}
	if _, err = c.Write([]byte(lines[0] + lines[1])); err != nil {
		t.Fatal(err)
	}
	scan := bufio.NewScanner(c)
	for range lines {
		if !scan.Scan() {
			t.Fatal("missing pipelined reply", scan.Err())
		}
	}
	for _, want := range lines {
		select {
		case got := <-records:
			if !bytes.Equal(got.Raw, []byte(want)) {
				t.Fatalf("forwarded %q want %q", got.Raw, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("missing forwarded request")
		}
	}
	select {
	case w := <-warnings:
		if w != "server.stop" {
			t.Fatal(w)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("missing management warning")
	}
}

func TestUnavailableRepliesUseCommittedErrorShape(t *testing.T) {
	s, err := Start(context.Background(), Options{Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("absent") }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	var doc any
	_ = json.Unmarshal(fakeherdr.Schema(), &doc)
	compiler := jsonschema.NewCompiler()
	_ = compiler.AddResource("https://adapter.test/schema.json", doc)
	schema, err := compiler.Compile("https://adapter.test/schema.json#/schemas/error_response")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		_, _ = c.Write([]byte(`{"id":"` + id + `","method":"ping","params":{}}` + "\n"))
		var value map[string]any
		if err = json.NewDecoder(c).Decode(&value); err != nil {
			t.Fatal(err)
		}
		if err = schema.Validate(value); err != nil {
			t.Fatal(err)
		}
		if value["id"] != id || value["error"].(map[string]any)["code"] != "unavailable" {
			t.Fatal(value)
		}
	}
}
