// Package hypr talks to Hyprland over its IPC socket.
package hypr

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client sends requests to one Hyprland instance.
type Client struct{ socket string }

// New finds the running Hyprland instance (from HYPRLAND_INSTANCE_SIGNATURE,
// or the first one under $XDG_RUNTIME_DIR/hypr when run outside the session).
func New() (*Client, error) {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	if sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"); sig != "" {
		return &Client{socket: filepath.Join(rt, "hypr", sig, ".socket.sock")}, nil
	}
	socks, _ := filepath.Glob(filepath.Join(rt, "hypr", "*", ".socket.sock"))
	for _, s := range socks {
		if c, err := net.DialTimeout("unix", s, 200*time.Millisecond); err == nil {
			c.Close()
			return &Client{socket: s}, nil
		}
	}
	return nil, fmt.Errorf("no running Hyprland instance found")
}

func (c *Client) request(cmd string) ([]byte, error) {
	conn, err := net.DialTimeout("unix", c.socket, time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil, err
	}
	return io.ReadAll(conn)
}

// Monitor is one output in global layout coordinates (logical pixels).
type Monitor struct {
	Name   string  `json:"name"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"` // logical size (pixels / scale)
	Height float64 `json:"height"`
}

// Monitors lists the active outputs.
func (c *Client) Monitors() ([]Monitor, error) {
	out, err := c.request("j/monitors")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name      string  `json:"name"`
		X         float64 `json:"x"`
		Y         float64 `json:"y"`
		Width     float64 `json:"width"`
		Height    float64 `json:"height"`
		Scale     float64 `json:"scale"`
		Transform int     `json:"transform"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse monitors: %w", err)
	}
	mons := make([]Monitor, 0, len(raw))
	for _, m := range raw {
		if m.Scale <= 0 {
			m.Scale = 1
		}
		w, h := m.Width/m.Scale, m.Height/m.Scale
		if m.Transform%2 == 1 { // rotated 90/270
			w, h = h, w
		}
		mons = append(mons, Monitor{Name: m.Name, X: m.X, Y: m.Y, Width: w, Height: h})
	}
	return mons, nil
}

// CursorPos returns the cursor position in global layout coordinates.
func (c *Client) CursorPos() (x, y float64, err error) {
	out, err := c.request("j/cursorpos")
	if err != nil {
		return 0, 0, err
	}
	var p struct{ X, Y float64 }
	if err := json.Unmarshal(out, &p); err != nil {
		return 0, 0, fmt.Errorf("parse cursorpos: %w", err)
	}
	return p.X, p.Y, nil
}

// Eval runs Lua in Hyprland (0.55+ Lua config API).
func (c *Client) Eval(lua string) error {
	out, err := c.request("eval " + lua)
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(out)); s != "ok" {
		return fmt.Errorf("hyprland: %s", s)
	}
	return nil
}

// MoveCursor warps the cursor to global layout coordinates.
func (c *Client) MoveCursor(x, y float64) error {
	return c.Eval(fmt.Sprintf("hl.dispatch(hl.dsp.cursor.move({x=%d, y=%d}))", int(x), int(y)))
}

// InactiveTimeout reads cursor:inactive_timeout (seconds, 0 = never hide).
func (c *Client) InactiveTimeout() (float64, error) {
	out, err := c.request("j/getoption cursor:inactive_timeout")
	if err != nil {
		return 0, err
	}
	var o struct {
		Float float64 `json:"float"`
		Int   float64 `json:"int"`
	}
	if err := json.Unmarshal(out, &o); err != nil {
		return 0, err
	}
	return o.Float + o.Int, nil
}

// SetInactiveTimeout sets cursor:inactive_timeout. A short timeout hides the
// cursor while our grab keeps Hyprland from seeing any pointer motion.
func (c *Client) SetInactiveTimeout(sec float64) error {
	return c.Eval(fmt.Sprintf("hl.config({ cursor = { inactive_timeout = %g } })", sec))
}
