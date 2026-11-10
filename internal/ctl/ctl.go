// Package ctl lets other programs (scripts, desktopctrl, AI agents) drive the
// peer through a running GhostKVM: type, press keys, move and click. It is a
// unix socket readable only by the current user; `ghostkvm ctl ...` is the
// client.
//
// Protocol: one JSON request per connection, one JSON reply.
//
//	{"cmd":"type","text":"hello"}        -> {"ok":true}
//	{"cmd":"status"}                     -> {"ok":true,"status":{...}}
package ctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/highercomve/GhostKVM/internal/keymap"
	"github.com/highercomve/GhostKVM/internal/kvm"
)

// gap between HID reports so BLE delivers them in order without queueing up
const gap = 12 * time.Millisecond

// SocketPath is where the control socket lives.
func SocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return filepath.Join(dir, "ghostkvm", "ctl.sock")
}

// Request is one command.
type Request struct {
	Cmd    string  `json:"cmd"`
	Text   string  `json:"text,omitempty"`
	Combo  string  `json:"combo,omitempty"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Button string  `json:"button,omitempty"`
	Amount int     `json:"amount,omitempty"`
	Target string  `json:"target,omitempty"` // switch: "peer" or "desktop"
}

// Reply is the answer to a request.
type Reply struct {
	OK     bool        `json:"ok"`
	Error  string      `json:"error,omitempty"`
	Status *kvm.Status `json:"status,omitempty"`
}

// Serve listens on the control socket until the listener is closed.
func Serve(k *kvm.KVM) (net.Listener, error) {
	path := SocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("another GhostKVM is already running (%s)", path)
	}
	os.Remove(path) // stale socket from a crash
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(k, c)
		}
	}()
	log.Printf("control socket: %s", path)
	return ln, nil
}

func handle(k *kvm.KVM, c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Minute)) // long texts take a while to type
	var req Request
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		json.NewEncoder(c).Encode(Reply{Error: "bad request: " + err.Error()})
		return
	}
	rep := Reply{OK: true}
	if st, err := run(k, req); err != nil {
		rep = Reply{Error: err.Error()}
	} else {
		rep.Status = st
	}
	json.NewEncoder(c).Encode(rep)
}

func run(k *kvm.KVM, r Request) (*kvm.Status, error) {
	hid := k.PeerHID()
	needPeer := func() error {
		if !hid.HostSubscribed() {
			return kvm.ErrNotConnected
		}
		return nil
	}
	switch r.Cmd {
	case "status":
		st, err := k.Status()
		return &st, err

	case "type":
		if err := needPeer(); err != nil {
			return nil, err
		}
		for _, ch := range r.Text {
			u, shift, ok := keymap.CharUsage(ch)
			if !ok {
				return nil, fmt.Errorf("can't type %q (only US-keyboard ASCII is supported)", ch)
			}
			var mods byte
			if shift {
				mods = keymap.ModLShift
			}
			hid.KeyReport(mods, u)
			time.Sleep(gap)
			hid.KeyReport(0)
			time.Sleep(gap)
		}
		return nil, nil

	case "key":
		mods, u, err := keymap.ParseCombo(r.Combo)
		if err != nil {
			return nil, err
		}
		if err := needPeer(); err != nil {
			return nil, err
		}
		if u == 0 {
			hid.KeyReport(mods) // modifiers only, e.g. "search"
		} else {
			hid.KeyReport(mods)
			time.Sleep(gap)
			hid.KeyReport(mods, u)
		}
		time.Sleep(gap)
		hid.KeyReport(0)
		return nil, nil

	case "move":
		return nil, k.MovePeerBy(r.X, r.Y)

	case "moveto":
		return nil, k.MovePeerTo(r.X, r.Y)

	case "click", "doubleclick":
		if err := needPeer(); err != nil {
			return nil, err
		}
		bit, err := buttonBit(r.Button)
		if err != nil {
			return nil, err
		}
		times := 1
		if r.Cmd == "doubleclick" {
			times = 2
		}
		for i := 0; i < times; i++ {
			hid.MouseMove(bit, 0, 0, 0)
			time.Sleep(gap)
			hid.MouseMove(0, 0, 0, 0)
			time.Sleep(gap * 4)
		}
		return nil, nil

	case "scroll":
		if err := needPeer(); err != nil {
			return nil, err
		}
		n := r.Amount
		step := 1
		if n < 0 {
			step, n = -1, -n
		}
		for i := 0; i < n; i++ {
			hid.MouseMove(0, 0, 0, int8(step))
			time.Sleep(gap)
		}
		return nil, nil

	case "switch":
		switch r.Target {
		case "peer", "chromebook", "remote":
			return nil, k.SetMode(true)
		case "desktop", "local", "here":
			return nil, k.SetMode(false)
		}
		return nil, fmt.Errorf("switch to \"peer\" or \"desktop\"")
	}
	return nil, fmt.Errorf("unknown command %q", r.Cmd)
}

func buttonBit(name string) (byte, error) {
	switch strings.ToLower(name) {
	case "", "left":
		return 1, nil
	case "right":
		return 2, nil
	case "middle":
		return 4, nil
	}
	return 0, fmt.Errorf("button must be left, right or middle")
}

// ---------------------------------------------------------------- client

const usage = `usage: ghostkvm ctl <command> [args]

Drive the other computer through a running GhostKVM. Works while your own
keyboard and mouse stay on this computer.

  status                       connection, who has control, layout
  type <text...>               type text (US keyboard layout)
  key <combo>                  e.g. ctrl+l, Return, alt+tab, search, ctrl+shift+t
  move <dx> <dy>               move the pointer by pixels
  moveto <x> <y>               move the pointer to a position (needs mouse acceleration off)
  click [left|right|middle]    click
  doubleclick [button]         double-click
  scroll <n>                   scroll wheel; positive is up, negative is down
  switch peer|desktop          give your keyboard and mouse to one side
`

// Main runs the `ghostkvm ctl` client and returns the process exit code.
func Main(args []string) int {
	req, err := parseArgs(args)
	if err != nil {
		if err.Error() != "" {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	rep, err := Send(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !rep.OK {
		fmt.Fprintln(os.Stderr, "ghostkvm:", rep.Error)
		return 1
	}
	if rep.Status != nil {
		st := rep.Status
		conn := "waiting"
		if st.Connected {
			conn = "connected"
		}
		control := "this computer"
		if st.Mode == "peer" {
			control = st.Config.Peer.Name
		}
		fmt.Printf("%s: %s\ncontrol: %s\nlayout: %s of %s, %vx%v\n",
			st.Config.Peer.Name, conn, control, st.Config.Peer.Side, st.Config.Peer.Monitor,
			st.Config.Peer.Width, st.Config.Peer.Height)
	}
	return 0
}

func parseArgs(args []string) (Request, error) {
	if len(args) == 0 {
		return Request{}, errors.New("missing command")
	}
	cmd, rest := args[0], args[1:]
	num := func(i int) (float64, error) {
		if i >= len(rest) {
			return 0, fmt.Errorf("%s needs %d numbers", cmd, i+1)
		}
		return strconv.ParseFloat(rest[i], 64)
	}
	r := Request{Cmd: cmd}
	switch cmd {
	case "status":
	case "type":
		if len(rest) == 0 {
			return r, errors.New("type needs text")
		}
		r.Text = strings.Join(rest, " ")
	case "key":
		if len(rest) != 1 {
			return r, errors.New("key needs one combo, like ctrl+l")
		}
		r.Combo = rest[0]
	case "move", "moveto":
		var err error
		if r.X, err = num(0); err != nil {
			return r, err
		}
		if r.Y, err = num(1); err != nil {
			return r, err
		}
	case "click", "doubleclick":
		if len(rest) > 0 {
			r.Button = rest[0]
		}
	case "scroll":
		n, err := num(0)
		if err != nil {
			return r, err
		}
		r.Amount = int(n)
	case "switch":
		if len(rest) != 1 {
			return r, errors.New("switch needs peer or desktop")
		}
		r.Target = rest[0]
	case "help", "-h", "--help":
		return r, errors.New("")
	default:
		return r, fmt.Errorf("unknown command %q", cmd)
	}
	return r, nil
}

// Send delivers one request to the running GhostKVM.
func Send(req Request) (Reply, error) {
	c, err := net.DialTimeout("unix", SocketPath(), 2*time.Second)
	if err != nil {
		return Reply{}, fmt.Errorf("GhostKVM is not running (no %s)", SocketPath())
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Reply{}, err
	}
	var rep Reply
	if err := json.NewDecoder(c).Decode(&rep); err != nil {
		return Reply{}, fmt.Errorf("no reply from GhostKVM: %w", err)
	}
	return rep, nil
}
