package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"yardr.dev/yardr/internal/version"
)

// Server is the local adapter over the contract: an MCP server on the
// official Go SDK (the modelcontextprotocol go-sdk, v1.7.0), serving
// the six tools of Tools over one connection. It speaks the
// modern revision (2026-07-28, no handshake, the version on each
// request) and the legacy revisions 2025-06-18 and 2025-11-25 (an
// initialize handshake, then the session it opens) from one handler,
// and no earlier one (FloorVersion).
//
// A weighed answer is one elicitation in both eras: the handler
// answers the call with an input-required result carrying the form,
// and the SDK carries it as a server elicitation request on a legacy
// session and as a multi-round-trip retry on a modern one, so this
// adapter writes the form once. Progress while ask_the_yardmaster
// waits is a notification tied to that call, in both eras.
type Server struct {
	Service *Service
}

// Instructions tell a client what this window is, in the person's
// words, as the server's own introduction beside its tools.
const Instructions = "This is the person's yard: where their work is kept and done, and where their questions and mail wait. " +
	"Speak of work, projects, questions and mail in the person's words. " +
	"What a tool result says is written for the person; say it to them as it stands."

// weighedSchema is the one field a weighty answer's form asks for: the
// question's number, typed back. A form is never asked for a secret:
// Service.Answer refuses those before a form exists.
var weighedSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"number": {"type": "string", "description": "the question's number, typed back exactly as shown"}
	},
	"required": ["number"]
}`)

// MCPServer builds the SDK server over this adapter's Service, with
// the contract's tools registered from Tools (so the served list and
// the contract cannot drift apart) and the floor at FloorVersion.
func (s *Server) MCPServer() *sdkmcp.Server {
	srv := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "yardr", Version: orVersion(version.Current())},
		&sdkmcp.ServerOptions{Instructions: Instructions},
	)
	srv.AddReceivingMiddleware(floorMiddleware)
	for _, def := range Tools() {
		def := def
		srv.AddTool(&sdkmcp.Tool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return s.call(ctx, req, def.Name)
		})
	}
	return srv
}

// Run serves over stdio, as yardr mcp is started: the standard input
// and output the MCP client started the process with. It serves until
// the client ends the connection or ctx ends.
func (s *Server) Run(ctx context.Context) error {
	return s.MCPServer().Run(ctx, &sdkmcp.StdioTransport{})
}

// Serve serves over one transport, for tests and for any other local
// carriage an adapter is built on.
func (s *Server) Serve(ctx context.Context, t sdkmcp.Transport) error {
	return s.MCPServer().Run(ctx, t)
}

func orVersion(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}

// floorMiddleware holds the floor at FloorVersion. The SDK negotiates
// the handshake itself, and no message of this server's is promised on
// a legacy one: a session that ends up below the floor (an initialize
// that offered 2025-03-26 or older, taken as the SDK takes it) is
// given no tool — its tool list is empty, and a tool call is refused.
// Where the modern era lets the server answer a request with an error
// (a request carrying its own version), that error carries
// FloorMessage in plain words.
func floorMiddleware(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
	return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
		if method != "tools/list" && method != "tools/call" {
			return next(ctx, method, req)
		}
		session, ok := req.GetSession().(*sdkmcp.ServerSession)
		if !ok {
			return next(ctx, method, req)
		}
		params := session.InitializeParams()
		if params == nil || params.ProtocolVersion == "" || params.ProtocolVersion >= FloorVersion {
			return next(ctx, method, req)
		}
		if method == "tools/list" {
			return &sdkmcp.ListToolsResult{Tools: []*sdkmcp.Tool{}}, nil
		}
		return nil, fmt.Errorf("%s", FloorMessage)
	}
}

// call is one tool call: its arguments by name, its Service method,
// its result as words with the structured part beside them, and the
// unread-mail line every result ends with.
func (s *Server) call(ctx context.Context, req *sdkmcp.CallToolRequest, name string) (*sdkmcp.CallToolResult, error) {
	if err := s.Service.requireUser(); err != nil {
		return toolError(err), nil
	}
	svc := *s.Service
	if svc.Wait <= 0 {
		svc.Wait = waitFor(clientName(req))
	}
	refusal := func(err error) (*sdkmcp.CallToolResult, error) {
		out := toolError(err)
		if line := s.unreadLine(ctx, &svc); line != "" {
			out.Content[0].(*sdkmcp.TextContent).Text += "\n\n" + line
		}
		return out, nil
	}
	args := map[string]any{}
	if raw := req.Params.Arguments; len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return refusal(fmt.Errorf("the arguments are not a JSON object: %w", err))
		}
		if args == nil {
			return refusal(fmt.Errorf("the arguments are not a JSON object"))
		}
		var trailing any
		if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = fmt.Errorf("more than one JSON value")
			}
			return refusal(fmt.Errorf("the arguments are not a JSON object: %w", err))
		}
	}
	res, err := s.dispatch(ctx, req, &svc, name, args)
	if err != nil {
		// A refusal is a result too: the unread line ends it as it
		// ends every other, while mail waits.
		return refusal(err)
	}
	if res == nil {
		return refusal(fmt.Errorf("the yard gave no answer"))
	}
	if res.Weighed != nil {
		return s.weighed(ctx, req, res.Weighed)
	}
	return s.finish(ctx, &svc, res), nil
}

// dispatch runs the Service method the tool name names.
func (s *Server) dispatch(ctx context.Context, req *sdkmcp.CallToolRequest, svc *Service, name string, args map[string]any) (*Result, error) {
	switch name {
	case ToolWhatIsHappening:
		return svc.WhatIsHappening(ctx, stringArg(args, "work"), stringArg(args, "since"))
	case ToolAskForSomething:
		return svc.AskForSomething(ctx, stringArg(args, "request"), stringArg(args, "project"))
	case ToolWhatWaitsForMe:
		return svc.WhatWaitsForMe(ctx)
	case ToolAnswer:
		in, err := answerInput(args)
		if err != nil {
			return nil, err
		}
		weighed, refusal, err := weighedWord(req, in.Question)
		if err != nil {
			return nil, err
		}
		if refusal != "" {
			return &Result{Text: refusal}, nil
		}
		return svc.Answer(ctx, in, weighed)
	case ToolAskTheYardmaster:
		return svc.AskTheYardmaster(ctx, stringArg(args, "message"), s.progress(ctx, req))
	case ToolReadMyMail:
		return svc.ReadMyMail(ctx)
	}
	return nil, fmt.Errorf("no tool of that name here")
}

// weighed answers a call Service left at a weighty card: the form goes
// to the person as an input request (which the SDK carries as an
// elicitation in the client's era), where the client can show one.
// Where it cannot, nothing is approved and the words say where the
// answer is given instead — rather than the call failing.
func (s *Server) weighed(ctx context.Context, req *sdkmcp.CallToolRequest, prompt *WeighedPrompt) (*sdkmcp.CallToolResult, error) {
	if !canElicit(req) {
		return s.finish(ctx, s.Service, &Result{Text: fmt.Sprintf(
			"That question carries weight, so it may only be approved where its weight is shown and its number typed back. "+
				"Your app cannot show that here, so nothing was approved and question %d is still open. "+
				"Answer it in the yard's window, and it will go through.", prompt.Number)}), nil
	}
	return &sdkmcp.CallToolResult{
		InputRequests: sdkmcp.InputRequestMap{
			"weighed": &sdkmcp.ElicitParams{
				Mode:            "form",
				Message:         prompt.Message,
				RequestedSchema: weighedSchema,
			},
		},
	}, nil
}

// weighedWord reads the typed-back number off a retried call: no
// input response yet (weighed false), one that accepts with the
// question's number (weighed true), and anything else a refusal in
// words, with nothing approved. The number is the person's own act,
// as it is at a terminal; a client that never retries has approved
// nothing.
func weighedWord(req *sdkmcp.CallToolRequest, question uint64) (weighed bool, refusal string, err error) {
	resp, ok := req.Params.InputResponses["weighed"]
	if !ok {
		return false, "", nil
	}
	el, ok := resp.(*sdkmcp.ElicitResult)
	if !ok {
		return false, "", fmt.Errorf("the weighed answer came back in a shape this yard does not read")
	}
	typed := ""
	if el.Action == "accept" && el.Content != nil {
		if v, ok := el.Content["number"].(string); ok {
			typed = v
		}
	}
	if typed != strconv.FormatUint(question, 10) {
		return false, fmt.Sprintf("The number typed back is not question %d's, so nothing was approved and the question is still open.", question), nil
	}
	return true, "", nil
}

// canElicit says whether this call's client can be shown a form: the
// elicitation capability it declared, in whichever era declared it.
func canElicit(req *sdkmcp.CallToolRequest) bool {
	if req.Session == nil {
		return false
	}
	params := req.Session.InitializeParams()
	if params == nil || params.Capabilities == nil {
		return false
	}
	return params.Capabilities.Elicitation != nil
}

// clientName is the client's name for this call, "" where it gave
// none: what the interim wait is chosen by.
func clientName(req *sdkmcp.CallToolRequest) string {
	if req.Session == nil {
		return ""
	}
	if params := req.Session.InitializeParams(); params != nil && params.ClientInfo != nil {
		return params.ClientInfo.Name
	}
	return ""
}

// waitFor is the interim bounded wait by client (design yardr-ts4d
// 3.5): the measured client's two minutes, and under a minute for
// every other, until a hand-run trial sets the other number.
func waitFor(client string) time.Duration {
	name := strings.ToLower(client)
	if strings.Contains(name, "claude") && strings.Contains(name, "code") {
		return DefaultWait
	}
	return ShortWait
}

// progress is the line ask_the_yardmaster stands under while its call
// waits: a notification tied to that call, carrying its progress
// token where the client gave one.
func (s *Server) progress(ctx context.Context, req *sdkmcp.CallToolRequest) func(string) {
	token := req.Params.GetProgressToken()
	return func(msg string) {
		if token == nil || req.Session == nil {
			return
		}
		_ = req.Session.NotifyProgress(ctx, &sdkmcp.ProgressNotificationParams{
			ProgressToken: token,
			Message:       msg,
		})
	}
}

// unreadLine is the unread-mail line as this connection's yard
// stands: "" where no mail waits, and where there is no user to count
// for.
func (s *Server) unreadLine(ctx context.Context, svc *Service) string {
	n, err := svc.UnreadCount(ctx)
	if err != nil {
		return ""
	}
	return Footer(n)
}

// finish is a finished result: its words, its structured part beside
// them, and the unread-mail line every result ends with while mail
// waits (which a read of the yard works out, and an unread count of
// none leaves unsaid).
func (s *Server) finish(ctx context.Context, svc *Service, res *Result) *sdkmcp.CallToolResult {
	text := res.Text
	if line := s.unreadLine(ctx, svc); line != "" {
		text += "\n\n" + line
	}
	out := &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
	}
	if res.Structured != nil {
		out.StructuredContent = res.Structured
	}
	return out
}

// toolError is a refusal as a tool result: words the person reads,
// marked as an error so the client does not mistake them for an
// answer, never a protocol failure.
func toolError(err error) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
		IsError: true,
	}
}

func stringArg(args map[string]any, name string) string {
	v, _ := args[name].(string)
	return v
}

// answerInput reads the answer tool's arguments into an AnswerInput,
// refusing a shape the tool does not take in plain words.
func answerInput(args map[string]any) (AnswerInput, error) {
	in := AnswerInput{Work: stringArg(args, "work"), Words: stringArg(args, "words")}
	question, ok := args["question"].(json.Number)
	if !ok {
		return in, fmt.Errorf("question: say its number, from what_waits_for_me")
	}
	var err error
	in.Question, err = strconv.ParseUint(string(question), 10, 64)
	if err != nil || in.Question == 0 {
		return in, fmt.Errorf("question: say its number, from what_waits_for_me")
	}
	if value, ok := args["pick"]; ok {
		pick, ok := value.(json.Number)
		if !ok {
			return in, fmt.Errorf("choice: say its number, from the question's choices")
		}
		n, err := strconv.ParseUint(string(pick), 10, 64)
		if err != nil || n == 0 || n > uint64(^uint(0)>>1) {
			return in, fmt.Errorf("choice: say its number, from the question's choices")
		}
		in.Pick = int(n)
	}
	in.Approve, _ = args["approve"].(bool)
	in.PutAside, _ = args["put_aside"].(bool)
	in.Decline, _ = args["decline"].(bool)
	return in, nil
}
