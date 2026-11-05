package cli

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/mcp"
)

// mcpServe serves the MCP server over the process's standard streams,
// as the client that started this process speaks over them. Tests hand
// it a carriage of their own (see TestMCPServeOverStdio).
var mcpServe = func(ctx context.Context, server *mcp.Server) error {
	return server.Run(ctx)
}

// yardr mcp is the yard served to a person's own MCP client on this
// machine (design yardr-ts4d, bead 1): a local stdio MCP server in
// internal/mcp, on the official Go SDK, acting as the user this run
// resolves to, exactly as yardr chat does. The tool contract (names,
// schemas, plain-words results) is internal/mcp's, defined once; this
// command is the adapter that resolves who it acts as and serves it.

func (a *app) mcpCmd() *cobra.Command {
	var connection string
	cmd := &cobra.Command{
		Use:   "mcp [--connection <name>]",
		Short: "Serve the yard to your own MCP client on this machine, over stdio",
		Long: `Runs an MCP server on the standard input and output your MCP client
started this process with, so that client (Claude Code, Claude Desktop,
or any other MCP client) works the yard as you: what is happening with
your work, filing a request, the questions waiting for you and your
answers to them, writing to the yardmaster, and your unread mail. It
is one more window like the browser view and yardr chat: every call is
made as the one user named below, and the yard's own rules decide each
one.

Who it acts as is who this run acts as, resolved as every yardr
command resolves it: --actor where that flag is given, else
$YARDR_ACTOR, else your login name, looked up as a person of the yard.
Where that names no person of the yard, the server runs and its tools
are listed, but every tool refuses with the reason and the way on, and
no tool is served until one resolves.

--connection names this client, once, in its configuration (for
example --connection openclaw): the name goes on every call, and the
yard's history marks what this client writes as you via that name
("ada via openclaw"), as it marks a device's writes. It is a label,
taken on trust with nothing behind it, since whoever can run this
command on this machine already acts as you; it cannot be revoked,
and stopping this client is stopping the program. Without a name,
writes read as you alone.

One thing is never done here, on any connection: a secret is never
given through MCP. A question that asks for one is refused in plain
words naming where it is given instead (the yard's browser view, or
yardr bead answer in a terminal here), and the question stays open.

Built on the official Go SDK for MCP (the modelcontextprotocol
go-sdk, v1.7.0). It speaks MCP 2025-06-18 or later: the modern
revision 2026-07-28 (no handshake, the version on each request) and
the legacy revisions 2025-06-18 and 2025-11-25 (a handshake, then
its session). ` + mcp.FloorMessage,
		Example: `  In a client's configuration (the shape most clients take):
    {"mcpServers": {"yardr": {"command": "yardr", "args": ["mcp", "--connection", "openclaw"]}}}`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			me, err := a.actingUser(ctx, st)
			if err != nil {
				return err
			}
			user := ""
			if me != nil {
				user = me.Name
			}
			server := &mcp.Server{Service: &mcp.Service{
				Yard:       st,
				User:       user,
				Connection: connection,
			}}
			if err := mcpServe(ctx, server); err != nil {
				// A client that goes away ends the server: that is
				// the session over, not a failure of the yard.
				if errors.Is(err, io.EOF) || errors.Is(ctx.Err(), context.Canceled) {
					return nil
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&connection, "connection", "", "the name this client is known by in the yard's history (for example openclaw)")
	return cmd
}
