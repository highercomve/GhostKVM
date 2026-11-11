// GhostKVM USB bridge firmware for the ESP32-S3.
//
// Receives keyboard and mouse commands from GhostKVM on UART0 (the board's
// "UART" port) using the CH9329 serial protocol, and replays them on the
// native USB port as a standard HID keyboard plus relative and absolute
// mouse. Speaking the CH9329 protocol means GhostKVM drives this board and a
// real CH9329 cable with the same code.
//
// Frame: 57 AB | addr | cmd | len | data[len] | sum (all bytes mod 256).
// Every command is answered with cmd|0x80 (or cmd|0xC0 with an error code).

#include <Arduino.h>
#include "USB.h"
#include "USBHID.h"

extern "C" bool tud_mounted(void);

namespace {

constexpr uint32_t kBaud = 115200;

enum : uint8_t {
  CMD_GET_INFO = 0x01,
  CMD_KB_GENERAL = 0x02,
  CMD_MS_ABS = 0x04,
  CMD_MS_REL = 0x05,
  CMD_RESET = 0x0F,
  CMD_CONSUMER = 0x40,  // GhostKVM extension: 16-bit consumer usage (volume, mute, play)
};

enum : uint8_t {
  ST_OK = 0x00,
  ST_ERR_CMD = 0xE3,
  ST_ERR_SUM = 0xE4,
  ST_ERR_PARA = 0xE5,
};

enum : uint8_t { RID_KEYBOARD = 1, RID_MOUSE = 2, RID_ABS = 3, RID_CONSUMER = 4 };

// clang-format off
const uint8_t kReportDescriptor[] = {
  // Keyboard: modifiers, reserved, 6 keys
  0x05, 0x01, 0x09, 0x06, 0xA1, 0x01, 0x85, RID_KEYBOARD,
  0x05, 0x07, 0x19, 0xE0, 0x29, 0xE7, 0x15, 0x00, 0x25, 0x01, 0x75, 0x01, 0x95, 0x08, 0x81, 0x02,
  0x95, 0x01, 0x75, 0x08, 0x81, 0x01,
  0x95, 0x06, 0x75, 0x08, 0x15, 0x00, 0x26, 0xFF, 0x00, 0x05, 0x07, 0x19, 0x00, 0x2A, 0xFF, 0x00, 0x81, 0x00,
  0xC0,
  // Relative mouse: buttons, x, y, wheel (int8)
  0x05, 0x01, 0x09, 0x02, 0xA1, 0x01, 0x85, RID_MOUSE, 0x09, 0x01, 0xA1, 0x00,
  0x05, 0x09, 0x19, 0x01, 0x29, 0x05, 0x15, 0x00, 0x25, 0x01, 0x95, 0x05, 0x75, 0x01, 0x81, 0x02,
  0x95, 0x01, 0x75, 0x03, 0x81, 0x03,
  0x05, 0x01, 0x09, 0x30, 0x09, 0x31, 0x09, 0x38, 0x15, 0x81, 0x25, 0x7F, 0x75, 0x08, 0x95, 0x03, 0x81, 0x06,
  0xC0, 0xC0,
  // Absolute mouse: buttons, x, y (0..4095), wheel
  0x05, 0x01, 0x09, 0x02, 0xA1, 0x01, 0x85, RID_ABS, 0x09, 0x01, 0xA1, 0x00,
  0x05, 0x09, 0x19, 0x01, 0x29, 0x05, 0x15, 0x00, 0x25, 0x01, 0x95, 0x05, 0x75, 0x01, 0x81, 0x02,
  0x95, 0x01, 0x75, 0x03, 0x81, 0x03,
  0x05, 0x01, 0x09, 0x30, 0x09, 0x31, 0x16, 0x00, 0x00, 0x26, 0xFF, 0x0F, 0x75, 0x10, 0x95, 0x02, 0x81, 0x02,
  0x09, 0x38, 0x15, 0x81, 0x25, 0x7F, 0x75, 0x08, 0x95, 0x01, 0x81, 0x06,
  0xC0, 0xC0,
  // Consumer control: one 16-bit usage (volume up 0xE9, mute 0xE2, ...)
  0x05, 0x0C, 0x09, 0x01, 0xA1, 0x01, 0x85, RID_CONSUMER,
  0x15, 0x00, 0x26, 0xFF, 0x03, 0x19, 0x00, 0x2A, 0xFF, 0x03, 0x75, 0x10, 0x95, 0x01, 0x81, 0x00,
  0xC0,
};
// clang-format on

USBHID hid;

class GhostHID : public USBHIDDevice {
 public:
  GhostHID() {
    static bool added = false;
    if (!added) {
      added = true;
      USBHID::addDevice(this, sizeof(kReportDescriptor));
    }
  }
  uint16_t _onGetDescriptor(uint8_t* buffer) override {
    memcpy(buffer, kReportDescriptor, sizeof(kReportDescriptor));
    return sizeof(kReportDescriptor);
  }
  // Host -> device output report: keyboard LEDs (num/caps/scroll lock).
  void _onOutput(uint8_t report_id, const uint8_t* buffer, uint16_t len) override {
    if (report_id == RID_KEYBOARD && len >= 1) leds = buffer[0];
  }
  bool send(uint8_t id, const void* data, size_t len) { return hid.SendReport(id, data, len, 20); }
  volatile uint8_t leds = 0;
};

GhostHID device;

// ---------------------------------------------------------------- framing

void reply(uint8_t addr, uint8_t cmd, const uint8_t* data, uint8_t len) {
  uint8_t frame[5 + 255 + 1] = {0x57, 0xAB, addr, cmd, len};
  memcpy(frame + 5, data, len);
  uint8_t sum = 0;
  for (int i = 0; i < 5 + len; i++) sum += frame[i];
  frame[5 + len] = sum;
  Serial.write(frame, 6 + len);
}

void ack(uint8_t addr, uint8_t cmd, uint8_t status) {
  uint8_t s = status;
  reply(addr, status == ST_OK ? (cmd | 0x80) : (cmd | 0xC0), &s, 1);
}

void handle(uint8_t addr, uint8_t cmd, const uint8_t* d, uint8_t len) {
  switch (cmd) {
    case CMD_GET_INFO: {
      // version, USB enumerated (1 = ready), LED state, 'G' marks GhostKVM firmware
      uint8_t info[8] = {0x30, (uint8_t)(tud_mounted() ? 1 : 0), device.leds, 'G', 0, 0, 0, 0};
      reply(addr, cmd | 0x80, info, sizeof(info));
      return;
    }
    case CMD_KB_GENERAL:
      if (len != 8) return ack(addr, cmd, ST_ERR_PARA);
      device.send(RID_KEYBOARD, d, 8);
      return ack(addr, cmd, ST_OK);
    case CMD_MS_REL: {
      // 01 btn dx dy wheel
      if (len != 5 || d[0] != 0x01) return ack(addr, cmd, ST_ERR_PARA);
      uint8_t r[4] = {d[1], d[2], d[3], d[4]};
      device.send(RID_MOUSE, r, sizeof(r));
      return ack(addr, cmd, ST_OK);
    }
    case CMD_MS_ABS: {
      // 02 btn xl xh yl yh wheel
      if (len != 7 || d[0] != 0x02) return ack(addr, cmd, ST_ERR_PARA);
      uint8_t r[6] = {d[1], d[2], d[3], d[4], d[5], d[6]};
      device.send(RID_ABS, r, sizeof(r));
      return ack(addr, cmd, ST_OK);
    }
    case CMD_CONSUMER:
      if (len != 2) return ack(addr, cmd, ST_ERR_PARA);
      device.send(RID_CONSUMER, d, 2);
      return ack(addr, cmd, ST_OK);
    case CMD_RESET:
      ack(addr, cmd, ST_OK);
      Serial.flush();
      ESP.restart();
      return;
  }
  ack(addr, cmd, ST_ERR_CMD);
}

// Frame parser state machine; resynchronises on the 57 AB header.
struct Parser {
  uint8_t buf[5 + 255 + 1];
  int n = 0;

  void feed(uint8_t b) {
    if (n == 0 && b != 0x57) return;
    if (n == 1 && b != 0xAB) { n = (b == 0x57) ? 1 : 0; return; }
    buf[n++] = b;
    if (n < 5) return;
    int total = 5 + buf[4] + 1;
    if (n < total) return;
    uint8_t sum = 0;
    for (int i = 0; i < total - 1; i++) sum += buf[i];
    if (sum == buf[total - 1]) handle(buf[2], buf[3], buf + 5, buf[4]);
    else ack(buf[2], buf[3], ST_ERR_SUM);
    n = 0;
  }
} parser;

}  // namespace

void setup() {
  Serial.setRxBufferSize(1024);
  Serial.begin(kBaud);
  USB.productName("GhostKVM");
  USB.manufacturerName("GhostKVM");
  hid.begin();
  USB.begin();
}

void loop() {
  while (Serial.available()) parser.feed((uint8_t)Serial.read());
  delay(0);
}
