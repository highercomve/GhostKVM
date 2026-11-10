// ghostkvm shares this Hyprland desktop's keyboard and mouse with a peer
// (e.g. a Chromebook) that sees the desktop as a Bluetooth keyboard+mouse.
// Push the mouse off the configured screen edge, or press the hotkey, to
// move control to the peer; come back the same way.
//
//	ghostkvm             tray icon + settings window
//	ghostkvm -headless   no window, for machines without a desktop
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/highercomve/GhostKVM/internal/blehid"
	"github.com/highercomve/GhostKVM/internal/config"
	"github.com/highercomve/GhostKVM/internal/gui"
	"github.com/highercomve/GhostKVM/internal/hypr"
	"github.com/highercomve/GhostKVM/internal/kvm"
)

func main() {
	cfgPath := flag.String("config", config.Path(), "config file (created with defaults if missing)")
	headless := flag.Bool("headless", false, "run without the tray icon and window")
	flag.Parse()
	log.SetFlags(log.Ltime)
	blehid.Logf = log.Printf

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("config: %s", *cfgPath)

	hy, err := hypr.New()
	if err != nil {
		log.Fatal(err)
	}

	p, err := blehid.New(cfg.BluetoothName)
	if err != nil {
		log.Fatal(err)
	}
	if err := p.Start(); err != nil {
		p.Stop()
		log.Fatal(err)
	}
	defer p.Stop()

	k, err := kvm.New(cfg, p, hy)
	if err != nil {
		log.Fatal(err)
	}

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
