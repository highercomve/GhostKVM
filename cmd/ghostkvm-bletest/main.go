// ghostkvm-bletest checks that this machine can act as a Bluetooth
// keyboard+mouse for another machine (e.g. a Chromebook) while keeping its
// own Bluetooth devices connected.
//
//	ghostkvm-bletest [-watch AA:BB:CC:DD:EE:FF] [-type]
//
// Pair "GhostKVM" from the other machine; once it subscribes, its pointer
// moves in a small square every 2 seconds.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/highercomve/GhostKVM/internal/blehid"
)

func main() {
	name := flag.String("name", "GhostKVM", "Bluetooth name to advertise")
	watch := flag.String("watch", "", "MAC of an already-connected device to monitor (e.g. your mouse)")
	typeText := flag.Bool("type", false, "also type 'ghost' on the host every 8 seconds")
	flag.Parse()

	log.SetFlags(log.Ltime)
	blehid.Logf = log.Printf

	p, err := blehid.New(*name)
	if err != nil {
		log.Fatal(err)
	}
	if err := p.Start(); err != nil {
		p.Stop()
		log.Fatal(err)
	}
	defer p.Stop()
	log.Printf("adapter %s ready - on the Chromebook open Settings > Bluetooth > Pair new device", p.Adapter())

	go logConnections(p.Conn())
	go exitOnBluezRestart(p.Conn())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(2 * time.Second)
	watchTick := time.NewTicker(10 * time.Second)
	if *watch != "" {
		reportWatch(p, *watch)
	}

	square := [][2]int8{{4, 0}, {0, 4}, {-4, 0}, {0, -4}}
	for i := 0; ; {
		select {
		case <-sig:
			log.Print("stopping")
			return
		case <-watchTick.C:
			if *watch != "" {
				reportWatch(p, *watch)
			}
		case <-tick.C:
			if !p.HostSubscribed() {
				continue
			}
			d := square[i%4]
			for j := 0; j < 10; j++ {
				p.MouseMove(0, int16(d[0]), int16(d[1]), 0)
				time.Sleep(10 * time.Millisecond)
			}
			log.Printf("sent mouse move (%d,%d) to host", int(d[0])*10, int(d[1])*10)
			i++
			if *typeText && i%4 == 0 {
				for _, k := range []byte{0x0A, 0x0B, 0x12, 0x16, 0x17} { // g h o s t
					p.KeyReport(0, k)
					p.KeyReport(0)
				}
				log.Print("typed 'ghost' on host")
			}
		}
	}
}

func reportWatch(p *blehid.Peripheral, mac string) {
	obj := p.Conn().Object("org.bluez", p.DevicePath(mac))
	name, err1 := obj.GetProperty("org.bluez.Device1.Alias")
	conn, err2 := obj.GetProperty("org.bluez.Device1.Connected")
	if err1 != nil || err2 != nil {
		log.Printf("watch %s: not found (%v)", mac, firstErr(err1, err2))
		return
	}
	log.Printf("watch: %v connected=%v", name.Value(), conn.Value())
}

func logConnections(conn *dbus.Conn) {
	for _, m := range [][]dbus.MatchOption{
		{dbus.WithMatchSender("org.bluez"), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged")},
		{dbus.WithMatchSender("org.bluez"), dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"), dbus.WithMatchMember("InterfacesAdded")},
	} {
		if err := conn.AddMatchSignal(m...); err != nil {
			log.Printf("cannot watch connections: %v", err)
			return
		}
	}
	ch := make(chan *dbus.Signal, 16)
	conn.Signal(ch)
	for s := range ch {
		if s.Name == "org.freedesktop.DBus.ObjectManager.InterfacesAdded" && len(s.Body) == 2 {
			ifs, _ := s.Body[1].(map[string]map[string]dbus.Variant)
			if d, ok := ifs["org.bluez.Device1"]; ok {
				log.Printf("new device %v name=%v connected=%v", s.Body[0], d["Alias"].Value(), d["Connected"].Value())
			}
			continue
		}
		if len(s.Body) < 2 || s.Body[0] != "org.bluez.Device1" {
			continue
		}
		changed, _ := s.Body[1].(map[string]dbus.Variant)
		for _, key := range []string{"Connected", "Paired", "Bonded", "ServicesResolved"} {
			if v, ok := changed[key]; ok {
				log.Printf("%s=%v: %s", key, v.Value(), s.Path)
			}
		}
	}
}

// exitOnBluezRestart exits when bluetoothd goes away: our registrations die
// with it, so a wrapper loop should start us again.
func exitOnBluezRestart(conn *dbus.Conn) {
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, "org.bluez"),
	); err != nil {
		log.Printf("cannot watch bluetoothd: %v", err)
		return
	}
	ch := make(chan *dbus.Signal, 4)
	conn.Signal(ch)
	for s := range ch {
		if s.Name == "org.freedesktop.DBus.NameOwnerChanged" && len(s.Body) == 3 && s.Body[0] == "org.bluez" {
			log.Print("bluetoothd restarted, exiting so we can be started again")
			os.Exit(3)
		}
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return fmt.Errorf("unknown")
}
