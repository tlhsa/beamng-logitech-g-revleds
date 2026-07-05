# BeamNG.drive - Logitech G rev LEDs (G29 / G27 / G920)

Drives the rev/shift LEDs on a Logitech G steering wheel from BeamNG.drive engine
RPM. It has two small parts:

1. **BeamNG mod** (`revleds` UDP protocol) - every frame it sends `rpm` + `maxRPM`
   (redline) plus a few extras to `127.0.0.1:4463` over UDP.
2. **`logi-revleds.exe`** (Windows helper) - listens for that data, computes how
   many LEDs to light, and writes a raw HID report (`F8 12 ...`) to the wheel. It
   **does not conflict** with the game using the wheel for input/FFB (separate HID
   channel).

BeamNG's built-in OutGauge protocol sends RPM but not the redline, so a rev-LED
bar cannot be scaled per car - hence the tiny custom protocol.

**Dynamic scaling:** the LEDs are not tied to a fixed RPM; they light relative to
each car's own **idle -> redline** range, so the feel stays the same when you
switch cars. The first LED comes on at the `-start` fraction of that band
(default `0.10`, i.e. just above idle), LEDs fill toward the redline, and all of
them flash at the `-shift` point (default 97% of redline). Example: a car with
idle 800 / redline 8750 gets the first LED at ~1595 rpm and flashing at ~8490 rpm.
The app prints the derived band to the console whenever the car changes.

## Installation

Prebuilt binaries are **not** committed to this repo (it holds source only).
Download the latest `logitech_g_revleds.zip` and `logi-revleds.exe` from the
[Releases page][releases], or build them yourself (see [Development](#development))
- both paths produce the same two files.

[releases]: https://github.com/tlhsa/beamng-logitech-g-revleds/releases

### 1) Mod
Copy `logitech_g_revleds.zip` into your BeamNG mods folder:
```
%LocalAppData%\BeamNG\BeamNG.drive\current\mods\
```
**Restart BeamNG** (or refresh the mod list in the Mod Manager) so the game mounts
it. Note: the `protocols_others_enabled` setting is ON by default, so no extra
configuration is needed.

### 2) Helper program
`logi-revleds.exe` is a single file with no dependencies to install.

## Running
1. Start BeamNG and get into a car.
2. Run `logi-revleds.exe`.
3. Rev the engine - the LEDs fill with RPM and flash at the redline.

Press Ctrl+C to quit; the LEDs are cleared on exit.

## Command-line options
```
logi-revleds.exe [options]

  -list           List Logitech HID interfaces and exit
  -probe          Run an LED test pattern on the selected interface and exit
  -index N        HID interface index (from -list) to use
  -path <path>    Explicit HID device path to use
  -port N         UDP port to listen on (default 4463)
  -leds N         Number of rev LEDs (default 5)
  -start F        First-LED position as a fraction of the idle->redline band
                  (0 = idle, 1 = redline; default 0.10 = just above idle)
  -shift F        Fraction of redline where all LEDs flash (default 0.97)
  -flashhz F      Flash frequency (Hz) at redline (default 12)
  -timeout N      Milliseconds without data before LEDs turn off (default 600)
  -v              Verbose telemetry logging
  -vid / -pid     USB vendor/product id (default 0x046D / 0xC24F = G29)
```

### Examples
```powershell
# Show interfaces
.\logi-revleds.exe -list

# Test LEDs on a specific interface (to find which one drives the LEDs)
.\logi-revleds.exe -probe -index 0

# Start the LEDs almost at idle
.\logi-revleds.exe -start 0.05

# Start later, only near the redline
.\logi-revleds.exe -start 0.4
```

## Troubleshooting
- **No LEDs:** run `-list`, then `-probe -index <N>` on each G29 interface
  (PID `0xC24F`) until the LEDs react; use that one permanently with `-index <N>`.
- **No data:** did you restart BeamNG? Are you seated in a car? If you changed the
  port, it must match in both the mod (`getPort` in
  `mod/lua/vehicle/protocols/revleds.lua`) and `-port`.
- **G HUB conflict:** disable any LED/RPM profile for the wheel in Logitech G HUB.

## Development

### Change the default `-start` value
The default lives in `app/main.go`:
```go
startF = flag.Float64("start", 0.10, "first LED position ...")
```
Change `0.10` to whatever you like (e.g. `0.05` for earlier, `0.25` for later),
then rebuild. (You can also override it at runtime with `-start` without editing.)

### Build the helper (single exe, no external dependencies)
Requires the Go toolchain (https://go.dev/dl/). Then:
```powershell
cd app
go build -o logi-revleds.exe .

# optional: also refresh the copy in dist\
Copy-Item logi-revleds.exe ..\dist\logi-revleds.exe -Force
```

### Package the mod
The mod source is under `mod\` (`lua\...` + `mod_info\...`). Repack it into
`dist\logitech_g_revleds.zip` with the helper script (it writes forward-slash
ZIP paths, which BeamNG requires):
```powershell
powershell -ExecutionPolicy Bypass -File tools\pack-mod.ps1
```
Then copy `dist\logitech_g_revleds.zip` into your BeamNG `mods\` folder and
restart the game (or refresh the mod list).

> **Note:** `dist\` is a generated output folder and is git-ignored. The
> `logi-revleds.exe` and `logitech_g_revleds.zip` it produces are exactly the two
> files to attach when publishing a [GitHub Release][releases].

## Mod source layout
```
mod\
  lua\vehicle\protocols\revleds.lua     # the UDP telemetry protocol
  mod_info\logig29revleds\info.json     # Mod Manager metadata (title/description)
```

## Telemetry payload (UDP struct, 32 bytes, little-endian)
| offset | type    | field      | description                                  |
|-------:|---------|------------|----------------------------------------------|
| 0      | uint32  | magic      | 0x47454C31                                   |
| 4      | float32 | rpm        | current rpm (smoothed)                       |
| 8      | float32 | maxRPM     | rev-limiter / redline                        |
| 12     | float32 | idleRPM    | idle rpm                                      |
| 16     | int32   | gear       | gear (reverse = -1, neutral = 0, 1..n)       |
| 20     | uint32  | flags      | bit0 engine running, bit1 should-shift, bit2 ignition |
| 24     | float32 | throttle   | 0..1                                          |
| 28     | uint32  | vehicleId  | vehicle object id                            |
