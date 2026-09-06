package mcpmock_test

import (
	"context"
	"fmt"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// Example_embedded shows the intended in-process (embedded) use of the facade:
// construct a Server on an ephemeral loopback port, start it, drive a request
// against an instance's URL with a real MCP client, and shut it down. This is
// the shape a hub test suite uses through StartTest (MOCK-107); the manual
// New/Start/Close form here keeps the example self-contained and free of a
// *testing.T so it runs as a documentation example.
func Example_embedded() {
	// A deterministic seed makes the run reproducible (MOCK-704).
	srv, err := mcpmock.New(
		mcpmock.WithSeed(42),
		mcpmock.WithAddr("127.0.0.1:0"),
	)
	if err != nil {
		panic(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		panic(err)
	}
	defer func() { _ = srv.Close() }()

	inst, ok := srv.Instance("default")
	if !ok {
		panic("default instance missing")
	}

	client, err := mcpclient.NewHTTP(inst.URL())
	if err != nil {
		panic(err)
	}
	defer func() { _ = client.Close() }()

	resp, err := client.ListTools(context.Background(), mcpclient.IntID(1))
	if err != nil {
		panic(err)
	}

	fmt.Println("got response:", resp.GotResponse)
	fmt.Println("is error:", resp.Envelope.Error != nil)
	fmt.Println("journal records:", inst.Journal().Len())

	// Output:
	// got response: true
	// is error: false
	// journal records: 1
}
