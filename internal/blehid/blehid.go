// Package blehid makes the local Bluetooth adapter appear as a BLE
// keyboard + mouse (HID over GATT) to another machine, using BlueZ over D-Bus.
package blehid

import (
	"fmt"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	bluezBus     = "org.bluez"
	ifaceOM      = "org.freedesktop.DBus.ObjectManager"
	ifaceProps   = "org.freedesktop.DBus.Properties"
	ifaceAdapter = "org.bluez.Adapter1"
	ifaceDevice  = "org.bluez.Device1"
	ifaceGattMgr = "org.bluez.GattManager1"
	ifaceAdvMgr  = "org.bluez.LEAdvertisingManager1"
	ifaceSvc     = "org.bluez.GattService1"
	ifaceChr     = "org.bluez.GattCharacteristic1"
	ifaceDsc     = "org.bluez.GattDescriptor1"
	ifaceAdv     = "org.bluez.LEAdvertisement1"
	ifaceAgent   = "org.bluez.Agent1"
	ifaceAgentMg = "org.bluez.AgentManager1"

	appPath   = dbus.ObjectPath("/org/ghostkvm")
	advPath   = dbus.ObjectPath("/org/ghostkvm/adv0")
	agentPath = dbus.ObjectPath("/org/ghostkvm/agent")

	kbdReportID   = 1
	mouseReportID = 2
)

// reportMap describes a keyboard (report 1, 8 bytes) and a 3-button mouse
// with 16-bit X/Y and a wheel (report 2, 6 bytes).
var reportMap = []byte{
	// Keyboard
	0x05, 0x01, 0x09, 0x06, 0xA1, 0x01, 0x85, kbdReportID,
	0x05, 0x07, 0x19, 0xE0, 0x29, 0xE7, 0x15, 0x00, 0x25, 0x01,
	0x75, 0x01, 0x95, 0x08, 0x81, 0x02, // modifiers
	0x95, 0x01, 0x75, 0x08, 0x81, 0x01, // reserved
	0x95, 0x06, 0x75, 0x08, 0x15, 0x00, 0x25, 0x65,
	0x05, 0x07, 0x19, 0x00, 0x29, 0x65, 0x81, 0x00, // 6 key slots
	0xC0,
	// Mouse
	0x05, 0x01, 0x09, 0x02, 0xA1, 0x01, 0x85, mouseReportID,
	0x09, 0x01, 0xA1, 0x00,
	0x05, 0x09, 0x19, 0x01, 0x29, 0x03, 0x15, 0x00, 0x25, 0x01,
	0x95, 0x03, 0x75, 0x01, 0x81, 0x02, // buttons
	0x95, 0x01, 0x75, 0x05, 0x81, 0x03, // padding
	0x05, 0x01, 0x09, 0x30, 0x09, 0x31, // X, Y: 16-bit relative, so any move fits one report
	0x16, 0x01, 0x80, 0x26, 0xFF, 0x7F, 0x75, 0x10, 0x95, 0x02, 0x81, 0x06,
	0x09, 0x38, // wheel: 8-bit relative
	0x15, 0x81, 0x25, 0x7F, 0x75, 0x08, 0x95, 0x01, 0x81, 0x06,
	0xC0, 0xC0,
}

// Logf is where the package reports events; replace to redirect.
var Logf = func(format string, args ...any) { fmt.Printf(format+"\n", args...) }

// ---------------------------------------------------------------- GATT objects

type service struct {
	path  dbus.ObjectPath
	uuid  string
	chars []*characteristic
}

func (s *service) props() map[string]dbus.Variant {
	paths := make([]dbus.ObjectPath, len(s.chars))
	for i, c := range s.chars {
		paths[i] = c.path
	}
	return map[string]dbus.Variant{
		"UUID":            dbus.MakeVariant(s.uuid),
		"Primary":         dbus.MakeVariant(true),
		"Characteristics": dbus.MakeVariant(paths),
	}
}

type characteristic struct {
	p         *Peripheral
	path      dbus.ObjectPath
	svc       *service
	uuid      string
	flags     []string
	descs     []*descriptor
	mu        sync.Mutex
	value     []byte
	notifying bool
}

func (c *characteristic) props() map[string]dbus.Variant {
	paths := make([]dbus.ObjectPath, len(c.descs))
	for i, d := range c.descs {
		paths[i] = d.path
	}
	return map[string]dbus.Variant{
		"Service":     dbus.MakeVariant(c.svc.path),
		"UUID":        dbus.MakeVariant(c.uuid),
		"Flags":       dbus.MakeVariant(c.flags),
		"Descriptors": dbus.MakeVariant(paths),
	}
}

// chrMethods is exported on D-Bus as org.bluez.GattCharacteristic1.
type chrMethods struct{ c *characteristic }

func (m chrMethods) ReadValue(opts map[string]dbus.Variant) ([]byte, *dbus.Error) {
	Logf("host read %s (%s)", m.c.uuid, m.c.path)
	m.c.mu.Lock()
	defer m.c.mu.Unlock()
	return append([]byte(nil), m.c.value...), nil
}

func (m chrMethods) WriteValue(v []byte, opts map[string]dbus.Variant) *dbus.Error {
	m.c.mu.Lock()
	m.c.value = append([]byte(nil), v...)
	m.c.mu.Unlock()
	Logf("host wrote %s: %x", m.c.uuid, v)
	return nil
}

func (m chrMethods) StartNotify() *dbus.Error {
	m.c.mu.Lock()
	m.c.notifying = true
	m.c.mu.Unlock()
	Logf("host subscribed to %s (%s)", m.c.uuid, m.c.path)
	return nil
}

func (m chrMethods) StopNotify() *dbus.Error {
	m.c.mu.Lock()
	m.c.notifying = false
	m.c.mu.Unlock()
	Logf("host unsubscribed from %s", m.c.uuid)
	return nil
}

func (c *characteristic) isNotifying() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.notifying
}

func (c *characteristic) notify(data []byte) error {
	c.mu.Lock()
	c.value = append([]byte(nil), data...)
	on := c.notifying
	c.mu.Unlock()
	if !on {
		return nil
	}
	return c.p.conn.Emit(c.path, ifaceProps+".PropertiesChanged", ifaceChr,
		map[string]dbus.Variant{"Value": dbus.MakeVariant(data)}, []string{})
}

type descriptor struct {
	path  dbus.ObjectPath
	chr   *characteristic
	uuid  string
	flags []string
	value []byte
}

func (d *descriptor) props() map[string]dbus.Variant {
	return map[string]dbus.Variant{
		"Characteristic": dbus.MakeVariant(d.chr.path),
		"UUID":           dbus.MakeVariant(d.uuid),
		"Flags":          dbus.MakeVariant(d.flags),
	}
}

type dscMethods struct{ d *descriptor }

func (m dscMethods) ReadValue(opts map[string]dbus.Variant) ([]byte, *dbus.Error) {
	Logf("host read descriptor %s (%s)", m.d.uuid, m.d.path)
	return m.d.value, nil
}

// propsObj serves org.freedesktop.DBus.Properties for one object.
type propsObj struct {
	iface string
	get   func() map[string]dbus.Variant
}

func (p propsObj) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != p.iface {
		return nil, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	return p.get(), nil
}

func (p propsObj) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	all, err := p.GetAll(iface)
	if err != nil {
		return dbus.Variant{}, err
	}
	v, ok := all[name]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	return v, nil
}

// objectManager serves GetManagedObjects for the whole GATT application.
type objectManager struct{ p *Peripheral }

func (o objectManager) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	out := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}
	for _, s := range o.p.services {
		out[s.path] = map[string]map[string]dbus.Variant{ifaceSvc: s.props()}
		for _, c := range s.chars {
			out[c.path] = map[string]map[string]dbus.Variant{ifaceChr: c.props()}
			for _, d := range c.descs {
				out[d.path] = map[string]map[string]dbus.Variant{ifaceDsc: d.props()}
			}
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- advertisement + agent

type advMethods struct{}

func (advMethods) Release() *dbus.Error { Logf("advertisement released by BlueZ"); return nil }

// agent auto-accepts "Just Works" pairing so the host can bond.
type agent struct{}

func (agent) Release() *dbus.Error { return nil }

// AuthorizeService only lets HID through. Without this, a paired host also
// connects audio profiles (A2DP, hands-free) over classic Bluetooth.
func (agent) AuthorizeService(dev dbus.ObjectPath, uuid string) *dbus.Error {
	switch strings.ToLower(uuid) {
	case "00001812-0000-1000-8000-00805f9b34fb", "00001124-0000-1000-8000-00805f9b34fb":
		Logf("authorized HID service %s for %s", uuid, dev)
		return nil
	}
	Logf("rejected service %s for %s", uuid, dev)
	return dbus.NewError("org.bluez.Error.Rejected", []any{"GhostKVM only provides HID"})
}
func (agent) RequestPinCode(dev dbus.ObjectPath) (string, *dbus.Error) { return "0000", nil }
func (agent) RequestPasskey(dev dbus.ObjectPath) (uint32, *dbus.Error) { return 0, nil }
func (agent) DisplayPinCode(dev dbus.ObjectPath, pin string) *dbus.Error {
	Logf("pin %s", pin)
	return nil
}
func (agent) DisplayPasskey(dev dbus.ObjectPath, pk uint32, entered uint16) *dbus.Error {
	Logf("passkey for %s: %06d", dev, pk)
	return nil
}
func (agent) RequestConfirmation(dev dbus.ObjectPath, pk uint32) *dbus.Error {
	Logf("auto-confirming pairing with %s (passkey %06d)", dev, pk)
	return nil
}
func (agent) RequestAuthorization(dev dbus.ObjectPath) *dbus.Error {
	Logf("auto-authorizing %s", dev)
	return nil
}
func (agent) Cancel() *dbus.Error { Logf("pairing cancelled"); return nil }

// ---------------------------------------------------------------- Peripheral

// Peripheral is a running BLE HID keyboard+mouse.
type Peripheral struct {
	conn     *dbus.Conn
	adapter  dbus.ObjectPath
	name     string
	services []*service
	kbd      *characteristic
	mouse    *characteristic
}

// New connects to BlueZ and prepares (but does not start) the peripheral.
func New(name string) (*Peripheral, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	p := &Peripheral{conn: conn, name: name}
	if p.adapter, err = p.findAdapter(); err != nil {
		conn.Close()
		return nil, err
	}
	p.build()
	return p, nil
}

func (p *Peripheral) Conn() *dbus.Conn         { return p.conn }
func (p *Peripheral) Adapter() dbus.ObjectPath { return p.adapter }
func (p *Peripheral) HostSubscribed() bool     { return p.mouse.isNotifying() || p.kbd.isNotifying() }

func (p *Peripheral) findAdapter() (dbus.ObjectPath, error) {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := p.conn.Object(bluezBus, "/").Call(ifaceOM+".GetManagedObjects", 0).Store(&objs); err != nil {
		return "", fmt.Errorf("query bluez: %w", err)
	}
	for path, ifs := range objs {
		_, g := ifs[ifaceGattMgr]
		_, a := ifs[ifaceAdvMgr]
		if g && a {
			return path, nil
		}
	}
	return "", fmt.Errorf("no BLE adapter with GATT server + advertising support")
}

func (p *Peripheral) addService(uuid string) *service {
	s := &service{path: dbus.ObjectPath(fmt.Sprintf("%s/svc%d", appPath, len(p.services))), uuid: uuid}
	p.services = append(p.services, s)
	return s
}

func (p *Peripheral) addChar(s *service, uuid string, flags []string, value []byte) *characteristic {
	c := &characteristic{
		p: p, svc: s, uuid: uuid, flags: flags, value: value,
		path: dbus.ObjectPath(fmt.Sprintf("%s/chr%d", s.path, len(s.chars))),
	}
	s.chars = append(s.chars, c)
	return c
}

func addDesc(c *characteristic, uuid string, flags []string, value []byte) {
	c.descs = append(c.descs, &descriptor{
		chr: c, uuid: uuid, flags: flags, value: value,
		path: dbus.ObjectPath(fmt.Sprintf("%s/dsc%d", c.path, len(c.descs))),
	})
}

func (p *Peripheral) build() {
	hid := p.addService("1812")
	p.addChar(hid, "2a4a", []string{"read"}, []byte{0x11, 0x01, 0x00, 0x02})      // HID information
	p.addChar(hid, "2a4b", []string{"encrypt-read"}, reportMap)                   // report map
	p.addChar(hid, "2a4c", []string{"write-without-response"}, []byte{0})         // control point
	p.addChar(hid, "2a4e", []string{"read", "write-without-response"}, []byte{1}) // protocol mode: report
	p.kbd = p.addChar(hid, "2a4d", []string{"encrypt-read", "notify"}, make([]byte, 8))
	addDesc(p.kbd, "2908", []string{"encrypt-read"}, []byte{kbdReportID, 1})
	p.mouse = p.addChar(hid, "2a4d", []string{"encrypt-read", "notify"}, make([]byte, 6))
	addDesc(p.mouse, "2908", []string{"encrypt-read"}, []byte{mouseReportID, 1})

	bat := p.addService("180f")
	p.addChar(bat, "2a19", []string{"read", "notify"}, []byte{100})
}

func (p *Peripheral) export() error {
	if err := p.conn.Export(objectManager{p}, appPath, ifaceOM); err != nil {
		return err
	}
	for _, s := range p.services {
		s := s
		if err := p.conn.Export(propsObj{ifaceSvc, s.props}, s.path, ifaceProps); err != nil {
			return err
		}
		for _, c := range s.chars {
			if err := p.conn.Export(chrMethods{c}, c.path, ifaceChr); err != nil {
				return err
			}
			if err := p.conn.Export(propsObj{ifaceChr, c.props}, c.path, ifaceProps); err != nil {
				return err
			}
			for _, d := range c.descs {
				if err := p.conn.Export(dscMethods{d}, d.path, ifaceDsc); err != nil {
					return err
				}
				if err := p.conn.Export(propsObj{ifaceDsc, d.props}, d.path, ifaceProps); err != nil {
					return err
				}
			}
		}
	}
	advProps := func() map[string]dbus.Variant {
		return map[string]dbus.Variant{
			"Type":         dbus.MakeVariant("peripheral"),
			"ServiceUUIDs": dbus.MakeVariant([]string{"1812", "180f"}),
			"Appearance":   dbus.MakeVariant(uint16(0x03C1)), // keyboard
			"LocalName":    dbus.MakeVariant(p.name),
			"Discoverable": dbus.MakeVariant(true),
		}
	}
	if err := p.conn.Export(advMethods{}, advPath, ifaceAdv); err != nil {
		return err
	}
	if err := p.conn.Export(propsObj{ifaceAdv, advProps}, advPath, ifaceProps); err != nil {
		return err
	}
	return p.conn.Export(agent{}, agentPath, ifaceAgent)
}

// Start registers the HID service, pairing agent and advertisement with BlueZ.
func (p *Peripheral) Start() error {
	if err := p.export(); err != nil {
		return fmt.Errorf("export objects: %w", err)
	}
	ad := p.conn.Object(bluezBus, p.adapter)
	for prop, v := range map[string]bool{"Powered": true, "Pairable": true} {
		if err := ad.SetProperty(ifaceAdapter+"."+prop, dbus.MakeVariant(v)); err != nil {
			return fmt.Errorf("set adapter %s: %w", prop, err)
		}
	}

	am := p.conn.Object(bluezBus, "/org/bluez")
	if err := am.Call(ifaceAgentMg+".RegisterAgent", 0, agentPath, "KeyboardDisplay").Err; err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	if err := am.Call(ifaceAgentMg+".RequestDefaultAgent", 0, agentPath).Err; err != nil {
		Logf("could not become default pairing agent (%v); you may need to confirm pairing elsewhere", err)
	}

	// Keep the classic Bluetooth side hidden: if the adapter is discoverable,
	// hosts merge the LE advertisement with the classic "computer" entry and
	// GhostKVM never shows up as a keyboard.
	if err := ad.SetProperty(ifaceAdapter+".Discoverable", dbus.MakeVariant(false)); err != nil {
		Logf("could not hide classic discoverability: %v", err)
	}
	if err := ad.Call(ifaceGattMgr+".RegisterApplication", 0, appPath, map[string]dbus.Variant{}).Err; err != nil {
		return fmt.Errorf("register GATT application: %w", err)
	}
	Logf("GATT HID service registered")
	if err := ad.Call(ifaceAdvMgr+".RegisterAdvertisement", 0, advPath, map[string]dbus.Variant{}).Err; err != nil {
		return fmt.Errorf("register advertisement: %w", err)
	}
	Logf("advertising as %q", p.name)
	return nil
}

// Stop unregisters everything from BlueZ. Bonds are kept.
func (p *Peripheral) Stop() {
	ad := p.conn.Object(bluezBus, p.adapter)
	ad.Call(ifaceAdvMgr+".UnregisterAdvertisement", 0, advPath)
	ad.Call(ifaceGattMgr+".UnregisterApplication", 0, appPath)
	ad.SetProperty(ifaceAdapter+".Discoverable", dbus.MakeVariant(false))
	p.conn.Object(bluezBus, "/org/bluez").Call(ifaceAgentMg+".UnregisterAgent", 0, agentPath)
	p.conn.Close()
}

// MouseMove sends relative motion and button state (bit0 left, bit1 right, bit2 middle).
func (p *Peripheral) MouseMove(buttons byte, dx, dy int16, wheel int8) error {
	return p.mouse.notify([]byte{buttons, byte(dx), byte(uint16(dx) >> 8), byte(dy), byte(uint16(dy) >> 8), byte(wheel)})
}

// KeyReport sends a full keyboard state: modifier bitmap + up to 6 HID usage codes.
func (p *Peripheral) KeyReport(mods byte, keys ...byte) error {
	r := make([]byte, 8)
	r[0] = mods
	copy(r[2:], keys)
	return p.kbd.notify(r)
}

// DevicePath returns the BlueZ object path for a MAC address on this adapter.
func (p *Peripheral) DevicePath(mac string) dbus.ObjectPath {
	return dbus.ObjectPath(fmt.Sprintf("%s/dev_%s", p.adapter, strings.ReplaceAll(strings.ToUpper(mac), ":", "_")))
}
