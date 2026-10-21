package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type AgentStatus string

const (
	StatusBlocked AgentStatus = "blocked"
	StatusWorking AgentStatus = "working"
	StatusDone    AgentStatus = "done"
	StatusIdle    AgentStatus = "idle"
	StatusUnknown AgentStatus = "unknown"
)

type WorktreeMetadata struct {
	CheckoutPath     string `json:"checkout_path"`
	IsLinkedWorktree bool   `json:"is_linked_worktree"`
	RepoKey          string `json:"repo_key"`
	RepoName         string `json:"repo_name"`
	RepoRoot         string `json:"repo_root"`
}

type WorkspaceInfo struct {
	WorkspaceID string            `json:"workspace_id"`
	Number      int               `json:"number"`
	Label       string            `json:"label"`
	Focused     bool              `json:"focused"`
	PaneCount   int               `json:"pane_count"`
	TabCount    int               `json:"tab_count"`
	ActiveTabID string            `json:"active_tab_id"`
	AgentStatus AgentStatus       `json:"agent_status"`
	Tokens      map[string]string `json:"tokens"`
	Worktree    *WorktreeMetadata `json:"worktree"`
}

func (w WorkspaceInfo) SpaceColorKey() string {
	for k := range w.Tokens {
		if strings.HasPrefix(k, "space_") {
			return k
		}
	}
	return "space_indigo"
}

type TabInfo struct {
	TabID       string      `json:"tab_id"`
	WorkspaceID string      `json:"workspace_id"`
	Number      int         `json:"number"`
	Label       string      `json:"label"`
	Focused     bool        `json:"focused"`
	PaneCount   int         `json:"pane_count"`
	AgentStatus AgentStatus `json:"agent_status"`
}

type AgentSession struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

type PaneInfo struct {
	PaneID                string            `json:"pane_id"`
	WorkspaceID           string            `json:"workspace_id"`
	TabID                 string            `json:"tab_id"`
	TerminalID            any               `json:"terminal_id"`
	Focused               bool              `json:"focused"`
	Cwd                   string            `json:"cwd"`
	ForegroundCwd         string            `json:"foreground_cwd"`
	Agent                 string            `json:"agent"`
	DisplayAgent          string            `json:"display_agent"`
	TerminalTitle         string            `json:"terminal_title"`
	TerminalTitleStripped string            `json:"terminal_title_stripped"`
	AgentStatus           AgentStatus       `json:"agent_status"`
	Tokens                map[string]string `json:"tokens"`
	AgentSession          *AgentSession     `json:"agent_session"`
	Revision              uint64            `json:"revision"`
}

type AgentInfo struct {
	PaneID                string            `json:"pane_id"`
	WorkspaceID           string            `json:"workspace_id"`
	TabID                 string            `json:"tab_id"`
	TerminalID            any               `json:"terminal_id"`
	Agent                 string            `json:"agent"`
	DisplayAgent          string            `json:"display_agent"`
	Name                  string            `json:"name"`
	AgentStatus           AgentStatus       `json:"agent_status"`
	Focused               bool              `json:"focused"`
	Title                 string            `json:"title"`
	TerminalTitle         string            `json:"terminal_title"`
	TerminalTitleStripped string            `json:"terminal_title_stripped"`
	Cwd                   string            `json:"cwd"`
	ForegroundCwd         string            `json:"foreground_cwd"`
	Tokens                map[string]string `json:"tokens"`
	AgentSession          *AgentSession     `json:"agent_session"`
	StateChangeSeq        uint64            `json:"state_change_seq"`
	Revision              uint64            `json:"revision"`
}

type SessionSnapshot struct {
	Version            string          `json:"version"`
	Protocol           int             `json:"protocol"`
	SessionName        string          `json:"session_name"`
	FocusedWorkspaceID string          `json:"focused_workspace_id"`
	FocusedTabID       string          `json:"focused_tab_id"`
	FocusedPaneID      string          `json:"focused_pane_id"`
	Workspaces         []WorkspaceInfo `json:"workspaces"`
	Tabs               []TabInfo       `json:"tabs"`
	Panes              []PaneInfo      `json:"panes"`
	Agents             []AgentInfo     `json:"agents"`
}

type PaneLayoutRect struct {
	PaneID  string `json:"pane_id"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Focused bool   `json:"focused"`
}

type PaneLayoutResult struct {
	WorkspaceID string           `json:"workspace_id"`
	TabID       string           `json:"tab_id"`
	LayoutMode  string           `json:"layout_mode"`
	Area        PaneLayoutRect   `json:"area"`
	Panes       []PaneLayoutRect `json:"panes"`
}

type SubscriptionEvent struct {
	Seq         uint64         `json:"seq"`
	TimestampMs int64          `json:"timestamp_ms"`
	Event       string         `json:"event"`
	WorkspaceID string         `json:"workspace_id"`
	TabID       string         `json:"tab_id"`
	PaneID      string         `json:"pane_id"`
	AgentStatus AgentStatus    `json:"agent_status"`
	Data        map[string]any `json:"data"`
}

func CleanInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0x2060 && r <= 0x206F {
			return -1
		}
		return r
	}, s)
}

func ResolveSocketPath() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	if cfg := os.Getenv("XDG_CONFIG_HOME"); cfg != "" {
		return filepath.Join(cfg, "herdr", "herdr.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

type Client struct {
	SocketPath string
	reqSeq     atomic.Uint64
}

func NewClient(socketPath string) *Client {
	if socketPath == "" {
		socketPath = ResolveSocketPath()
	}
	return &Client{SocketPath: socketPath}
}

type rpcRequest struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *Client) Call(method string, params map[string]any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	id := fmt.Sprintf("husk-herdr-%d-%d", os.Getpid(), c.reqSeq.Add(1))
	req := rpcRequest{
		ID:     id,
		Method: method,
		Params: params,
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	conn, err := net.DialTimeout("unix", c.SocketPath, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write(payload); err != nil {
		return err
	}

	reader := bufio.NewReaderSize(conn, 512*1024)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return err
	}

	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

func (c *Client) GetSnapshot() (*SessionSnapshot, error) {
	var wrapper struct {
		Type     string          `json:"type"`
		Snapshot SessionSnapshot `json:"snapshot"`
	}
	if err := c.Call("session.snapshot", nil, &wrapper); err != nil {
		return nil, err
	}
	snap := &wrapper.Snapshot
	if snap.SessionName == "" {
		snap.SessionName = "herdr"
	}
	for i := range snap.Workspaces {
		snap.Workspaces[i].Label = CleanInvisible(snap.Workspaces[i].Label)
		for k, v := range snap.Workspaces[i].Tokens {
			snap.Workspaces[i].Tokens[k] = CleanInvisible(v)
		}
	}
	for i := range snap.Tabs {
		snap.Tabs[i].Label = CleanInvisible(snap.Tabs[i].Label)
	}
	for i := range snap.Panes {
		snap.Panes[i].DisplayAgent = CleanInvisible(snap.Panes[i].DisplayAgent)
		snap.Panes[i].TerminalTitle = CleanInvisible(snap.Panes[i].TerminalTitle)
		snap.Panes[i].TerminalTitleStripped = CleanInvisible(snap.Panes[i].TerminalTitleStripped)
		for k, v := range snap.Panes[i].Tokens {
			snap.Panes[i].Tokens[k] = CleanInvisible(v)
		}
	}
	for i := range snap.Agents {
		snap.Agents[i].DisplayAgent = CleanInvisible(snap.Agents[i].DisplayAgent)
		snap.Agents[i].Title = CleanInvisible(snap.Agents[i].Title)
		snap.Agents[i].TerminalTitle = CleanInvisible(snap.Agents[i].TerminalTitle)
		snap.Agents[i].TerminalTitleStripped = CleanInvisible(snap.Agents[i].TerminalTitleStripped)
		for k, v := range snap.Agents[i].Tokens {
			snap.Agents[i].Tokens[k] = CleanInvisible(v)
		}
	}
	return snap, nil
}

func (c *Client) FocusWorkspace(workspaceID string) error {
	return c.Call("workspace.focus", map[string]any{"workspace_id": workspaceID}, nil)
}

func (c *Client) CreateWorkspace(label string) error {
	params := map[string]any{}
	if label != "" {
		params["label"] = label
	}
	return c.Call("workspace.create", params, nil)
}

func (c *Client) FocusTab(tabID string) error {
	return c.Call("tab.focus", map[string]any{"tab_id": tabID}, nil)
}

func (c *Client) FocusPane(paneID string) error {
	return c.Call("pane.focus", map[string]any{"pane_id": paneID}, nil)
}

func (c *Client) FocusAgent(paneID string) error {
	return c.Call("agent.focus", map[string]any{"pane_id": paneID}, nil)
}

func (c *Client) ReadPaneVisible(paneID string) (string, error) {
	var wrapper struct {
		Type string `json:"type"`
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	err := c.Call("pane.read", map[string]any{
		"pane_id":    paneID,
		"source":     "visible",
		"format":     "text",
		"strip_ansi": true,
	}, &wrapper)
	if err != nil {
		return "", err
	}
	return wrapper.Read.Text, nil
}

func (c *Client) GetPaneLayout(paneID string) (*PaneLayoutResult, error) {
	var raw struct {
		Layout struct {
			WorkspaceID string `json:"workspace_id"`
			TabID       string `json:"tab_id"`
			LayoutMode  string `json:"layout_mode"`
			Area        struct {
				X      int `json:"x"`
				Y      int `json:"y"`
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"area"`
			Panes []struct {
				PaneID  string `json:"pane_id"`
				Focused bool   `json:"focused"`
				Rect    struct {
					X      int `json:"x"`
					Y      int `json:"y"`
					Width  int `json:"width"`
					Height int `json:"height"`
				} `json:"rect"`
			} `json:"panes"`
		} `json:"layout"`
	}
	if err := c.Call("pane.layout", map[string]any{"pane_id": paneID}, &raw); err != nil {
		return nil, err
	}
	res := &PaneLayoutResult{
		WorkspaceID: raw.Layout.WorkspaceID,
		TabID:       raw.Layout.TabID,
		LayoutMode:  raw.Layout.LayoutMode,
		Area: PaneLayoutRect{
			X:      raw.Layout.Area.X,
			Y:      raw.Layout.Area.Y,
			Width:  raw.Layout.Area.Width,
			Height: raw.Layout.Area.Height,
		},
	}
	for _, p := range raw.Layout.Panes {
		res.Panes = append(res.Panes, PaneLayoutRect{
			PaneID:  p.PaneID,
			X:       p.Rect.X,
			Y:       p.Rect.Y,
			Width:   p.Rect.Width,
			Height:  p.Rect.Height,
			Focused: p.Focused,
		})
	}
	return res, nil
}

func (c *Client) SubscribeEvents(ctx context.Context, onEvent func(SubscriptionEvent)) error {
	conn, err := net.DialTimeout("unix", c.SocketPath, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	req := rpcRequest{
		ID:     fmt.Sprintf("sub-%d", os.Getpid()),
		Method: "events.subscribe",
		Params: map[string]any{
			"subscriptions": []map[string]any{
				{"type": "workspace.created"},
				{"type": "workspace.updated"},
				{"type": "workspace.renamed"},
				{"type": "workspace.closed"},
				{"type": "workspace.focused"},
				{"type": "tab.created"},
				{"type": "tab.closed"},
				{"type": "tab.focused"},
				{"type": "tab.renamed"},
				{"type": "pane.created"},
				{"type": "pane.closed"},
				{"type": "pane.focused"},
				{"type": "pane.moved"},
				{"type": "pane.exited"},
				{"type": "pane.agent_detected"},
			},
		},
	}
	payload, _ := json.Marshal(req)
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		return err
	}

	scanner := bufio.NewScanner(conn)
	buf := make([]byte, 0, 128*1024)
	scanner.Buffer(buf, 2*1024*1024)
	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if first {
			first = false
			continue
		}
		var ev SubscriptionEvent
		if err := json.Unmarshal(line, &ev); err == nil && ev.Event != "" {
			onEvent(ev)
		}
	}
	return scanner.Err()
}
