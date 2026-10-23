//go:build windows

package monitor

import (
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 的 raw ICMP socket 需要系統管理員權限；iphlpapi 的 IcmpSendEcho 則不需要，
// 因此 IPv4 目標一律改用它，一般使用者也能走 native。IPv6 仍用 pro-bing（需管理員），
// 失敗時照常退回系統 ping。
func init() {
	newEchoSession = newWindowsSession
	_, _, _ = procQueryPerfFrequency.Call(uintptr(unsafe.Pointer(&qpcFrequency))) // #nosec G103 -- Win32 API 以指標回傳結果
}

var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")

	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procQueryPerfCounter   = kernel32.NewProc("QueryPerformanceCounter")
	procQueryPerfFrequency = kernel32.NewProc("QueryPerformanceFrequency")
	qpcFrequency           int64
)

// qpcNow 讀取 QueryPerformanceCounter。Windows 上 time.Now 的單調時鐘解析度較粗，
// loopback 與區網的 RTT 會被量成 0，所以 RTT 改用 QPC 計時（解析度遠小於 1µs）。
func qpcNow() int64 {
	var c int64
	_, _, _ = procQueryPerfCounter.Call(uintptr(unsafe.Pointer(&c))) // #nosec G103 -- Win32 API 以指標回傳結果
	return c
}

func qpcSince(start int64) time.Duration {
	if qpcFrequency <= 0 {
		return 0
	}
	ticks := qpcNow() - start
	return time.Duration(ticks/qpcFrequency*int64(time.Second) + ticks%qpcFrequency*int64(time.Second)/qpcFrequency)
}

const (
	icmpPayloadSize = 32
	// ICMP_ECHO_REPLY（64 位元下 40 bytes）+ payload + 8 bytes ICMP 錯誤訊息空間，取寬鬆的上限
	icmpReplySize = 64 + icmpPayloadSize + 8
	ipSuccess     = 0 // ICMP_ECHO_REPLY.Status 的 IP_SUCCESS
)

func newWindowsSession(ip string, interval time.Duration, h echoHandlers) (echoSession, error) {
	v4 := net.ParseIP(ip).To4()
	if v4 == nil {
		return newProBingSession(ip, interval, h)
	}
	if err := procIcmpSendEcho.Find(); err != nil {
		return newProBingSession(ip, interval, h)
	}
	handle, _, callErr := procIcmpCreateFile.Call()
	if windows.Handle(handle) == windows.InvalidHandle {
		return nil, errors.Join(errors.New("IcmpCreateFile failed"), callErr)
	}
	return &icmpSession{
		handle:   handle,
		dst:      binary.LittleEndian.Uint32(v4), // IPAddr 為網路位元組順序，記憶體排列即 a.b.c.d
		interval: interval,
		timeout:  probeTimeout(interval),
		h:        h,
		stop:     make(chan struct{}),
	}, nil
}

// icmpSession 每個 interval 同步呼叫一次 IcmpSendEcho；序號由本端產生，
// 結算（成功／逾時／晚到）仍交給 runNative 的 probeTracker。
type icmpSession struct {
	handle   uintptr
	dst      uint32
	interval time.Duration
	timeout  time.Duration
	h        echoHandlers
	stop     chan struct{}
	once     sync.Once
}

func (s *icmpSession) Stop() {
	s.once.Do(func() { close(s.stop) })
}

func (s *icmpSession) Run() error {
	defer func() { _, _, _ = procIcmpCloseHandle.Call(s.handle) }()

	payload := make([]byte, icmpPayloadSize)
	reply := make([]byte, icmpReplySize)
	timeoutMs := uintptr(min(s.timeout.Milliseconds(), 60_000)) // #nosec G115 -- 已限制在 60 秒內

	for seq := 0; ; seq = (seq + 1) & 0xffff {
		select {
		case <-s.stop:
			return nil
		default:
		}

		start := time.Now()
		s.h.onSend(seq)
		qpcStart := qpcNow()
		n, _, _ := procIcmpSendEcho.Call(
			s.handle,
			uintptr(s.dst),
			uintptr(unsafe.Pointer(&payload[0])), // #nosec G103 -- 固定大小 buffer，呼叫期間存活
			uintptr(len(payload)),
			0,                                  // RequestOptions
			uintptr(unsafe.Pointer(&reply[0])), // #nosec G103 -- 固定大小 buffer，呼叫期間存活
			uintptr(len(reply)),
			timeoutMs,
		)
		// 以 QPC 實測 RTT：API 的 RoundTripTime 只有毫秒精度，區網會全變成 0 或 1
		rtt := qpcSince(qpcStart)
		if n > 0 && binary.LittleEndian.Uint32(reply[4:8]) == ipSuccess {
			s.h.onRecv(seq, rtt)
		}

		if wait := s.interval - time.Since(start); wait > 0 && !s.sleep(wait) {
			return nil
		}
	}
}

func (s *icmpSession) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.stop:
		return false
	case <-t.C:
		return true
	}
}
