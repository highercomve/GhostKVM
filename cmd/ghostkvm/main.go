// ghostkvm shares this desktop's keyboard and mouse with another device (a
// Chromebook, tablet, phone, Mac, PC...) that sees it as a Bluetooth keyboard
// and mouse.
// Push the mouse off the configured screen edge, or press the hotkey, to
// move control to the peer; come back the same way.
//
//	ghostkvm             tray icon + settings window
//	ghostkvm -headless   no window, for machines without a desktop
//	ghostkvm ctl ...     drive the other computer through a running GhostKVM
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/highercomve/GhostKVM/internal/blehid"
	"github.com/highercomve/GhostKVM/internal/config"
	"github.com/highercomve/GhostKVM/internal/ctl"
	"github.com/highercomve/GhostKVM/internal/gui"
	"github.com/highercomve/GhostKVM/internal/kvm"
	"github.com/highercomve/GhostKVM/internal/screen/auto"
	"github.com/highercomve/GhostKVM/internal/serialhid"
	"github.com/highercomve/GhostKVM/internal/update"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(ctl.Main(os.Args[2:]))
	}
	cfgPath := flag.String("config", config.Path(), "config file (created with defaults if missing)")
	headless := flag.Bool("headless", false, "run without the tray icon and window")
	flag.Parse()
	log.SetFlags(log.Ltime)
	blehid.Logf = log.Printf
	serialhid.Logf = log.Printf
	// After a self-update, the old version is still shutting down: wait for
	// it to hand back Bluetooth and the control socket.
	update.WaitForPrevious(15 * time.Second)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("config: %s", *cfgPath)

	scr, err := auto.Detect(cfg.ScreenBackend)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("desktop: %s (edge switching: %v)", scr.Name(), scr.EdgeSwitching())

	out, closeOut, problem := openOutput(cfg)
	defer closeOut()
	if problem != "" {
		log.Printf("output: %s", problem)
	}

	k, err := kvm.New(cfg, out, scr)
	if err != nil {
		log.Fatal(err)
	}
	k.SetOutputProblem(problem)

	ln, err := ctl.Serve(k)
	if err != nil {
		closeOut()
		log.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := k.Run(ctx); err != nil {
			log.Print(err)
		}
	}()

	if *headless {
		<-done
	} else {
		// Quit from the tray, or Ctrl+C / SIGTERM, both stop the KVM first so
		// the keyboard and mouse are always handed back to the desktop.
		go func() { <-ctx.Done(); gui.Quit() }()
		gui.Run(k, *cfgPath, func() { cancel(); <-done })
	}
	cancel()
	<-done
	log.Print("stopped")
}

// openOutput opens the configured way of reaching the other device. When the
// USB bridge can't be opened, GhostKVM still starts (so it can be fixed in
// Settings) with an output that is never connected, and problem says why.
func openOutput(cfg *config.Config) (kvm.HID, func(), string) {
	if cfg.Output == "usb" {
		if cfg.USBPort == "" {
			return disconnected{}, func() {}, serialhid.ErrNoPort.Error()
		}
		d, err := serialhid.Open(cfg.USBPort, cfg.USBBaud)
		if err != nil {
			return disconnected{}, func() {}, "USB bridge: " + err.Error()
		}
		log.Printf("output: USB bridge on %s at %d baud", cfg.USBPort, cfg.USBBaud)
		return d, func() { d.Close() }, ""
	}
	p, err := blehid.New(cfg.BluetoothName)
	if err == nil {
		if err = p.Start(); err != nil {
			p.Stop()
		}
	}
	if err != nil {
		return disconnected{}, func() {}, "Bluetooth: " + err.Error()
	}
	return p, p.Stop, ""
}

// disconnected is the output used when the configured one couldn't start.
type disconnected struct{}

func (disconnected) MouseMove(byte, int16, int16, int8) error { return nil }
func (disconnected) KeyReport(byte, ...byte) error            { return nil }
func (disconnected) HostSubscribed() bool                     { return false }
