// ghostkvm shares this Hyprland desktop's keyboard and mouse with a peer
// (e.g. a Chromebook) that sees the desktop as a Bluetooth keyboard+mouse.
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

	p, err := blehid.New(cfg.BluetoothName)
	if err != nil {
		log.Fatal(err)
	}
	if err := p.Start(); err != nil {
		p.Stop()
		log.Fatal(err)
	}
	defer p.Stop()

	k, err := kvm.New(cfg, p, scr)
	if err != nil {
		log.Fatal(err)
	}

	ln, err := ctl.Serve(k)
	if err != nil {
		p.Stop()
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
