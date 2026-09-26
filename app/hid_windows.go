//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	setupapi = syscall.NewLazyDLL("setupapi.dll")
	hiddll   = syscall.NewLazyDLL("hid.dll")

	procSetupDiGetClassDevsW             = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = setupapi.NewProc("SetupDiDestroyDeviceInfoList")

	procHidD_GetHidGuid        = hiddll.NewProc("HidD_GetHidGuid")
	procHidD_GetAttributes     = hiddll.NewProc("HidD_GetAttributes")
	procHidD_GetPreparsedData  = hiddll.NewProc("HidD_GetPreparsedData")
	procHidD_FreePreparsedData = hiddll.NewProc("HidD_FreePreparsedData")
	procHidP_GetCaps           = hiddll.NewProc("HidP_GetCaps")
	procHidD_SetOutputReport   = hiddll.NewProc("HidD_SetOutputReport")
	procHidD_SetFeature        = hiddll.NewProc("HidD_SetFeature")
)

const (
	digcfPresent         = 0x02
	digcfDeviceInterface = 0x10
	hidpStatusSuccess    = 0x00110000
)

type spDeviceInterfaceData struct {
	cbSize             uint32
	interfaceClassGuid syscall.GUID
	flags              uint32
	reserved           uintptr
}

type hiddAttributes struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
}

// HIDP_CAPS: 32 x USHORT = 64 bytes. Only the first fields are used here.
type hidpCaps struct {
	Usage                   uint16
	UsagePage               uint16
	InputReportByteLength   uint16
	OutputReportByteLength  uint16
	FeatureReportByteLength uint16
	Reserved                [17]uint16
	NumberLinkCollection    uint16
	NumberInputButtonCaps   uint16
	NumberInputValueCaps    uint16
	NumberInputDataIndices  uint16
	NumberOutputButtonCaps  uint16
	NumberOutputValueCaps   uint16
	NumberOutputDataIndices uint16
	NumberFeatureButton     uint16
	NumberFeatureValueCaps  uint16
	NumberFeatureDataIdx    uint16
}

type hidDevice struct {
	Path      string
	VID, PID  uint16
	Usage     uint16
	UsagePage uint16
	InLen     int
	OutLen    int
	FeatLen   int
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	// Measure up to the terminating NUL without uintptr arithmetic, then
	// convert in one step.
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; ptr = unsafe.Add(ptr, 2) {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// winErr formats the result of a Win32 BOOL-returning call. The syscall
// package renders Errno values with their Windows message (e.g. 87 ->
// "The parameter is incorrect."), which is what makes LED write failures
// diagnosable.
func winErr(what string, err error) error {
	if errno, ok := err.(syscall.Errno); ok && errno == 0 {
		return fmt.Errorf("%s returned FALSE (no Win32 error code)", what)
	}
	if err == nil {
		return fmt.Errorf("%s returned FALSE", what)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func openPath(path string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		// Some collections deny read/write; retry write-only.
		h, err = syscall.CreateFile(p,
			syscall.GENERIC_WRITE,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
			nil, syscall.OPEN_EXISTING, 0, 0)
	}
	return h, err
}

func queryDevice(path string) (hidDevice, error) {
	dev := hidDevice{Path: path}
	h, err := openPath(path)
	if err != nil {
		return dev, err
	}
	defer syscall.CloseHandle(h)

	var attr hiddAttributes
	attr.Size = uint32(unsafe.Sizeof(attr))
	if r, _, _ := procHidD_GetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attr))); r == 0 {
		return dev, fmt.Errorf("HidD_GetAttributes failed")
	}
	dev.VID = attr.VendorID
	dev.PID = attr.ProductID

	var pp uintptr
	if r, _, _ := procHidD_GetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&pp))); r != 0 && pp != 0 {
		var caps hidpCaps
		if st, _, _ := procHidP_GetCaps.Call(pp, uintptr(unsafe.Pointer(&caps))); uint32(st) == hidpStatusSuccess {
			dev.Usage = caps.Usage
			dev.UsagePage = caps.UsagePage
			dev.InLen = int(caps.InputReportByteLength)
			dev.OutLen = int(caps.OutputReportByteLength)
			dev.FeatLen = int(caps.FeatureReportByteLength)
		}
		procHidD_FreePreparsedData.Call(pp)
	}
	return dev, nil
}

// enumerate returns all present HID interfaces, optionally filtered by vendor id (0 = all).
func enumerate(vid uint16) ([]hidDevice, error) {
	var guid syscall.GUID
	procHidD_GetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))

	h, _, _ := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&guid)), 0, 0,
		uintptr(digcfPresent|digcfDeviceInterface))
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return nil, fmt.Errorf("SetupDiGetClassDevs failed")
	}
	defer procSetupDiDestroyDeviceInfoList.Call(h)

	var devs []hidDevice
	for idx := uint32(0); ; idx++ {
		var ifd spDeviceInterfaceData
		ifd.cbSize = uint32(unsafe.Sizeof(ifd))
		if r, _, _ := procSetupDiEnumDeviceInterfaces.Call(h, 0,
			uintptr(unsafe.Pointer(&guid)), uintptr(idx),
			uintptr(unsafe.Pointer(&ifd))); r == 0 {
			break // ERROR_NO_MORE_ITEMS
		}

		var reqSize uint32
		procSetupDiGetDeviceInterfaceDetailW.Call(h,
			uintptr(unsafe.Pointer(&ifd)), 0, 0,
			uintptr(unsafe.Pointer(&reqSize)), 0)
		if reqSize == 0 {
			continue
		}

		buf := make([]byte, reqSize)
		// cbSize of SP_DEVICE_INTERFACE_DETAIL_DATA_W: 8 on 64-bit, 6 on 32-bit.
		if unsafe.Sizeof(uintptr(0)) == 8 {
			*(*uint32)(unsafe.Pointer(&buf[0])) = 8
		} else {
			*(*uint32)(unsafe.Pointer(&buf[0])) = 6
		}
		if r, _, _ := procSetupDiGetDeviceInterfaceDetailW.Call(h,
			uintptr(unsafe.Pointer(&ifd)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(reqSize),
			0, 0); r == 0 {
			continue
		}
		// DevicePath (WCHAR[]) begins right after the cbSize DWORD, at offset 4.
		path := utf16PtrToString((*uint16)(unsafe.Pointer(&buf[4])))
		if path == "" {
			continue
		}

		dev, err := queryDevice(path)
		if err != nil {
			continue
		}
		if vid == 0 || dev.VID == vid {
			devs = append(devs, dev)
		}
	}
	return devs, nil
}

// ledWriter holds an open handle to the wheel and remembers which write method works.
type ledWriter struct {
	h       syscall.Handle
	outLen  int
	featLen int
	method  int // 0 unknown, 1 SetOutputReport, 2 WriteFile, 3 SetFeature
}

func openWriter(dev hidDevice) (*ledWriter, error) {
	h, err := openPath(dev.Path)
	if err != nil {
		return nil, err
	}
	return &ledWriter{h: h, outLen: dev.OutLen, featLen: dev.FeatLen}, nil
}

func (w *ledWriter) close() {
	if w != nil && w.h != syscall.InvalidHandle {
		syscall.CloseHandle(w.h)
		w.h = syscall.InvalidHandle
	}
}

// MethodName returns a human-readable name of the write path that succeeded.
func (w *ledWriter) MethodName() string {
	switch w.method {
	case 1:
		return "HidD_SetOutputReport"
	case 2:
		return "WriteFile"
	case 3:
		return "HidD_SetFeature"
	default:
		return "none"
	}
}

// The classic Logitech rev-LED command (in the Linux hid-lg4ff driver, which
// drives the LEDs on the G27 and G29):
//
//	F8 12 <bitmask> 00 00 00 01
//
// bitmask bits 0..4 map to the 5 rev LEDs (progressive fill). The command is a
// native output report that must be sent to the wheel's main joystick
// interface (usage page 0x01), not to its vendor-specific collections. Only
// the G29 is tested here; the G27 uses the same command so it should work. The
// G923 is unverified (the Xbox G923 uses HID++/TrueForce). The G920 has no rev
// LEDs.
func ledCommand(mask byte) []byte {
	return []byte{0xF8, 0x12, mask, 0x00, 0x00, 0x00, 0x01}
}

func padReport(cmd []byte, reportLen int) []byte {
	size := reportLen
	if size < len(cmd)+1 {
		size = len(cmd) + 1 // report id byte + payload
	}
	buf := make([]byte, size)
	buf[0] = 0x00 // report id (device uses unnumbered reports)
	copy(buf[1:], cmd)
	return buf
}

func (w *ledWriter) setOutputReport(buf []byte) error {
	r, _, err := procHidD_SetOutputReport.Call(uintptr(w.h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return winErr(fmt.Sprintf("HidD_SetOutputReport(%d bytes)", len(buf)), err)
	}
	return nil
}

func (w *ledWriter) setFeature(buf []byte) error {
	r, _, err := procHidD_SetFeature.Call(uintptr(w.h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return winErr(fmt.Sprintf("HidD_SetFeature(%d bytes)", len(buf)), err)
	}
	return nil
}

func (w *ledWriter) writeFile(buf []byte) error {
	var n uint32
	if err := syscall.WriteFile(w.h, buf, &n, nil); err != nil {
		return fmt.Errorf("WriteFile(%d bytes): %w", len(buf), err)
	}
	return nil
}

// setMask lights the given rev-LED bitmask, auto-detecting the transport on first use.
func (w *ledWriter) setMask(mask byte) error {
	cmd := ledCommand(mask)
	outBuf := padReport(cmd, w.outLen)
	featBuf := padReport(cmd, w.featLen)

	if w.method == 1 {
		return w.setOutputReport(outBuf)
	}
	if w.method == 2 {
		return w.writeFile(outBuf)
	}
	if w.method == 3 {
		return w.setFeature(featBuf)
	}

	// method unknown: try each until one succeeds, then latch it. Keep every
	// failure (including its Win32 code) so the caller can report the reason.
	var attempts []string
	err := w.setOutputReport(outBuf)
	if err == nil {
		w.method = 1
		return nil
	}
	attempts = append(attempts, err.Error())

	err = w.writeFile(outBuf)
	if err == nil {
		w.method = 2
		return nil
	}
	attempts = append(attempts, err.Error())

	err = w.setFeature(featBuf)
	if err == nil {
		w.method = 3
		return nil
	}
	attempts = append(attempts, err.Error())

	return fmt.Errorf("all HID write methods failed (out report %d bytes, feat report %d bytes): %s",
		len(outBuf), len(featBuf), strings.Join(attempts, "; "))
}
