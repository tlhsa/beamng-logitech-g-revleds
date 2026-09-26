//go:build windows

// logi-revleds - drives the rev LEDs on a Logitech G29 steering wheel from
// BeamNG.drive telemetry sent by the companion "revleds" UDP protocol mod.
// Only the G29 is tested; other Logitech G wheels with rev LEDs may work (see README).
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultVID = 0x046D // Logitech
	defaultPID = 0xC24F // G29 Driving Force Racing Wheel (the only tested wheel)
	// Other Logitech G wheels can be selected with -pid, but are UNTESTED:
	//   G27         0xC29B  (same F8 12 command in Linux hid-lg4ff; likely works)
	//   G923 PS/PC  0xC266  (unverified)
	//   G923 Xbox   0xC26E  (HID++/TrueForce - classic command probably won't work)
	// The G920 (0xC262) has no rev LEDs.
	packetSize = 32
	magic      = 0x47454C31
)

type telemetry struct {
	Magic     uint32
	RPM       float32
	MaxRPM    float32
	IdleRPM   float32
	Gear      int32
	Flags     uint32
	Throttle  float32
	VehicleID uint32
}

func parse(b []byte) (telemetry, bool) {
	if len(b) < packetSize {
		return telemetry{}, false
	}
	t := telemetry{
		Magic:     binary.LittleEndian.Uint32(b[0:]),
		RPM:       math.Float32frombits(binary.LittleEndian.Uint32(b[4:])),
		MaxRPM:    math.Float32frombits(binary.LittleEndian.Uint32(b[8:])),
		IdleRPM:   math.Float32frombits(binary.LittleEndian.Uint32(b[12:])),
		Gear:      int32(binary.LittleEndian.Uint32(b[16:])),
		Flags:     binary.LittleEndian.Uint32(b[20:]),
		Throttle:  math.Float32frombits(binary.LittleEndian.Uint32(b[24:])),
		VehicleID: binary.LittleEndian.Uint32(b[28:]),
	}
	return t, t.Magic == magic
}

type config struct {
	numLeds int
	startF  float64 // first-LED position as a fraction of the idle->redline band
	shiftF  float64 // flash-all point as a fraction of redline
	flashHz float64
	timeout time.Duration
}

func fullMask(n int) byte { return byte((1 << uint(n)) - 1) }

// computeMask maps telemetry to a rev-LED bitmask and whether it should flash.
//
// The rev bar is scaled to each car's OWN usable rev range: LEDs fill linearly
// from a point inside the idle->redline band up to the redline, and all LEDs
// flash at/above the shift point. Everything is derived from the values the car
// reports (idleRPM, maxRPM), so it adapts per vehicle instead of a fixed rpm.
func computeMask(t telemetry, cfg config) (mask byte, flashing bool) {
	// engine off -> everything dark (ignore residual/cranking rpm)
	if t.Flags&1 == 0 {
		return 0, false
	}
	maxRPM := float64(t.MaxRPM)
	if maxRPM <= 0 {
		maxRPM = 7000 // fallback when the car reported no redline
	}
	idle := float64(t.IdleRPM)
	if idle <= 0 || idle >= maxRPM {
		idle = maxRPM * 0.15 // fallback pseudo-idle when the car reported none
	}
	rpm := float64(t.RPM)

	// flash all LEDs at/after the shift point, or when the sim requests a shift
	flashAt := maxRPM * cfg.shiftF
	if rpm >= flashAt || t.Flags&2 != 0 {
		return fullMask(cfg.numLeds), true
	}

	// progressive fill across the car's own rev range: [low .. redline]
	low := idle + cfg.startF*(maxRPM-idle)
	high := maxRPM
	if high <= low {
		high = low + 1
	}
	if rpm <= low {
		return 0, false
	}
	frac := (rpm - low) / (high - low)
	n := int(math.Ceil(frac * float64(cfg.numLeds)))
	if n < 1 {
		n = 1
	}
	if n > cfg.numLeds {
		n = cfg.numLeds
	}
	return fullMask(n), false
}

func main() {
	var (
		list    = flag.Bool("list", false, "list Logitech HID interfaces and exit")
		probe   = flag.Bool("probe", false, "run an LED test pattern on the selected interface and exit")
		index   = flag.Int("index", -1, "HID interface index (from -list) to use")
		pathSel = flag.String("path", "", "explicit HID device path to use")
		port    = flag.Int("port", 4463, "UDP port to listen on")
		numLeds = flag.Int("leds", 5, "number of rev LEDs")
		startF  = flag.Float64("start", 0.10, "first LED position as a fraction of the idle->redline band (0=idle, 1=redline); low value = just above idle")
		shiftF  = flag.Float64("shift", 0.97, "fraction of redline where all LEDs flash")
		flashHz = flag.Float64("flashhz", 12, "flash frequency (Hz) at redline")
		timeout = flag.Int("timeout", 600, "milliseconds without data before LEDs turn off")
		verbose = flag.Bool("v", false, "verbose telemetry logging")
		vid     = flag.Int("vid", defaultVID, "USB vendor id")
		pid     = flag.Int("pid", defaultPID, "USB product id")
	)
	flag.Parse()

	devs, err := enumerate(uint16(*vid))
	if err != nil {
		fmt.Println("HID enumeration failed:", err)
		os.Exit(1)
	}

	if *list {
		printList(devs, uint16(*pid))
		return
	}

	dev, w, err := openWorkingWriter(devs, uint16(*pid), *index, *pathSel)
	if err != nil {
		fmt.Println("device selection failed:", err)
		fmt.Println("run with -list to see available interfaces")
		os.Exit(1)
	}
	defer w.close()
	fmt.Printf("using interface: %s\n  VID=%#04x PID=%#04x usagePage=%#x usage=%#x out=%d feat=%d transport=%s\n",
		dev.Path, dev.VID, dev.PID, dev.UsagePage, dev.Usage, dev.OutLen, dev.FeatLen, w.MethodName())

	cfg := config{
		numLeds: *numLeds,
		startF:  *startF,
		shiftF:  *shiftF,
		flashHz: *flashHz,
		timeout: time.Duration(*timeout) * time.Millisecond,
	}

	if *probe {
		runProbe(w, cfg)
		return
	}

	runLoop(w, cfg, *port, *verbose)
}

func printList(devs []hidDevice, pid uint16) {
	if len(devs) == 0 {
		fmt.Println("no matching Logitech HID interfaces found")
		return
	}
	// Find the interface auto-selection would try first.
	bestRank := 99
	for _, d := range devs {
		if r := deviceRank(d, pid); r < bestRank {
			bestRank = r
		}
	}
	best := -1
	if bestRank <= 4 {
		for i, d := range devs {
			if deviceRank(d, pid) == bestRank {
				best = i
				break
			}
		}
	}
	fmt.Printf("%-3s %-6s %-6s %-9s %-7s %-4s %-4s %-4s  %s\n",
		"idx", "VID", "PID", "usgPage", "usage", "in", "out", "feat", "path")
	for i, d := range devs {
		mark := " "
		switch {
		case i == best:
			mark = ">"
		case d.PID == pid:
			mark = "*"
		}
		fmt.Printf("%s%-2d 0x%04x 0x%04x 0x%-7x 0x%-5x %-4d %-4d %-4d  %s\n",
			mark, i, d.VID, d.PID, d.UsagePage, d.Usage, d.InLen, d.OutLen, d.FeatLen, d.Path)
	}
	fmt.Println("\n(> = interface auto-selection uses, * = matches the target PID; pick one with -index N or -path <path>)")
}

// deviceRank scores an interface for automatic selection (lower is better).
//
// The rev-LED command (F8 12 ...) is a native output report on the wheel's main
// joystick interface. On Windows that is the Generic Desktop / Joystick
// collection (usage page 0x01, usage 0x04; e.g. "mi_00"), the same one the
// Linux hid-lg4ff driver and other G29 tools use. Logitech wheels also expose
// vendor collections (usage page 0xFF00) that Windows lists with an output
// report length but which do not accept the LED command, so they rank lower.
func deviceRank(d hidDevice, pid uint16) int {
	switch {
	case d.PID == pid && d.UsagePage == 0x01 && d.Usage == 0x04:
		return 0 // main joystick interface (G29/G27 rev LEDs)
	case d.PID == pid && d.UsagePage == 0x01:
		return 1 // generic-desktop main interface (usage varies per wheel)
	case d.PID == pid && d.OutLen >= 8:
		return 2 // out-capable vendor collection: last-resort PID match
	case d.PID == pid:
		return 3
	case d.OutLen >= 8:
		return 4 // no PID match: anything that can take an output report
	default:
		return 5
	}
}

// candidateDevices returns the interfaces to try, best first. An explicit
// -index or -path restricts the list to exactly that interface.
func candidateDevices(devs []hidDevice, pid uint16, index int, path string) ([]hidDevice, error) {
	if path != "" {
		if d, err := queryDevice(path); err == nil {
			return []hidDevice{d}, nil
		}
		return []hidDevice{{Path: path, OutLen: 8, FeatLen: 8}}, nil
	}
	if index >= 0 {
		if index >= len(devs) {
			return nil, fmt.Errorf("index %d out of range (%d interfaces)", index, len(devs))
		}
		return []hidDevice{devs[index]}, nil
	}
	if len(devs) == 0 {
		return nil, fmt.Errorf("no Logitech HID interfaces present")
	}
	cands := append([]hidDevice(nil), devs...)
	sort.SliceStable(cands, func(i, j int) bool {
		return deviceRank(cands[i], pid) < deviceRank(cands[j], pid)
	})
	return cands, nil
}

// shortPath trims a device interface path to its informative part, e.g.
// "hid#vid_046d&pid_c24f&mi_00#7&3b2a7b87&1&0000".
func shortPath(p string) string {
	p = strings.TrimPrefix(p, `\\?\`)
	if i := strings.IndexByte(p, '{'); i > 0 {
		p = p[:i]
	}
	return strings.TrimSuffix(p, "#")
}

// openWorkingWriter picks a wheel interface and verifies it by writing an
// all-LEDs-off report. Auto-selection walks the ranked candidates until one
// accepts the LED command, so a writable-looking vendor collection cannot
// silently win. Every failed attempt is reported with its Win32 error.
func openWorkingWriter(devs []hidDevice, pid uint16, index int, path string) (hidDevice, *ledWriter, error) {
	cands, err := candidateDevices(devs, pid, index, path)
	if err != nil {
		return hidDevice{}, nil, err
	}
	var failures []string
	for _, d := range cands {
		w, err := openWriter(d)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: open failed: %v", shortPath(d.Path), err))
			continue
		}
		if err := w.setMask(0); err != nil {
			failures = append(failures, fmt.Sprintf("%s (usgPage=%#x usage=%#x out=%d): %v",
				shortPath(d.Path), d.UsagePage, d.Usage, d.OutLen, err))
			w.close()
			continue
		}
		return d, w, nil
	}
	return hidDevice{}, nil, fmt.Errorf("no interface accepted the LED command:\n    %s",
		strings.Join(failures, "\n    "))
}

func runProbe(w *ledWriter, cfg config) {
	fmt.Println("LED probe: filling 0 ->", cfg.numLeds, "then flashing. Watch the wheel...")
	if err := w.setMask(0); err != nil {
		fmt.Println("write failed:", err)
		return
	}
	fmt.Println("transport:", w.MethodName())
	time.Sleep(400 * time.Millisecond)
	for n := 1; n <= cfg.numLeds; n++ {
		if err := w.setMask(fullMask(n)); err != nil {
			fmt.Println("write failed:", err)
			return
		}
		fmt.Printf("  %d LED(s) -> mask %#02x\n", n, fullMask(n))
		time.Sleep(400 * time.Millisecond)
	}
	for i := 0; i < 6; i++ {
		m := byte(0)
		if i%2 == 0 {
			m = fullMask(cfg.numLeds)
		}
		w.setMask(m)
		time.Sleep(120 * time.Millisecond)
	}
	w.setMask(0)
	fmt.Println("probe done (LEDs cleared). transport used:", w.MethodName())
}

func runLoop(w *ledWriter, cfg config, port int, verbose bool) {
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Printf("cannot listen on udp 127.0.0.1:%d: %v\n", port, err)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Printf("listening for BeamNG telemetry on 127.0.0.1:%d ...\n", port)
	fmt.Println("drive in BeamNG; LEDs follow RPM. press Ctrl+C to quit.")

	var (
		mu       sync.Mutex
		latest   telemetry
		haveData bool
		lastData time.Time
	)

	var lastBandMax float32 = -1
	go func() {
		buf := make([]byte, 256)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if t, ok := parse(buf[:n]); ok {
				mu.Lock()
				latest, haveData, lastData = t, true, time.Now()
				mu.Unlock()
				// (re)print the derived band whenever the redline changes, i.e. on car switch
				if t.MaxRPM != lastBandMax {
					lastBandMax = t.MaxRPM
					idle := float64(t.IdleRPM)
					if idle <= 0 || idle >= float64(t.MaxRPM) {
						idle = float64(t.MaxRPM) * 0.15
					}
					low := idle + cfg.startF*(float64(t.MaxRPM)-idle)
					flash := float64(t.MaxRPM) * cfg.shiftF
					fmt.Printf("car: idle=%.0f redline=%.0f -> first LED @ %.0f rpm, flash @ %.0f rpm\n",
						idle, t.MaxRPM, low, flash)
				}
				if verbose {
					fmt.Printf("rpm=%.0f/%.0f gear=%d flags=%d thr=%.2f\n",
						t.RPM, t.MaxRPM, t.Gear, t.Flags, t.Throttle)
				}
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	tick := time.NewTicker(time.Second / 60)
	defer tick.Stop()

	flashOn := true
	lastFlash := time.Now()
	flashInterval := time.Duration(float64(time.Second) / (cfg.flashHz * 2))
	var lastMask byte = 0xFF // impossible value forces first write

	for {
		select {
		case <-sig:
			w.setMask(0)
			fmt.Println("\nbye (LEDs cleared)")
			return
		case now := <-tick.C:
			mu.Lock()
			t, hd, ld := latest, haveData, lastData
			mu.Unlock()

			var mask byte
			if !hd || now.Sub(ld) > cfg.timeout {
				mask = 0
				flashOn = true
			} else {
				m, flashing := computeMask(t, cfg)
				if flashing {
					if now.Sub(lastFlash) >= flashInterval {
						flashOn = !flashOn
						lastFlash = now
					}
					if flashOn {
						mask = m
					} else {
						mask = 0
					}
				} else {
					flashOn = true
					mask = m
				}
			}

			if mask != lastMask {
				if err := w.setMask(mask); err != nil {
					fmt.Println("LED write error:", err)
				}
				lastMask = mask
			}
		}
	}
}
