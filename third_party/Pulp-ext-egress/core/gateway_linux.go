//go:build linux

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// PreExecHook, if non-nil, is called inside the CHILD process after the
// network namespace and TUN interface are fully configured, right before
// syscall.Exec replaces the process image. Set this in main() of the binary
// that embeds both egress and confine; it fires in the re-exec'd child.
//
// If the hook returns an error, the child prints to stderr and exits 1
// (FAIL CLOSED — the target is never exec'd unconfined).
var PreExecHook func() error

const (
	gatewayIP = "10.0.0.1"
	childIP   = "10.0.0.2"
	tunMTU    = 1500
	nicID     = tcpip.NICID(1)
)

// allowedIPSet is a mutex-guarded set of IP addresses that may complete TCP connections.
type allowedIPSet struct {
	mu  sync.RWMutex
	set map[string]bool
}

func newAllowedIPSet() *allowedIPSet {
	return &allowedIPSet{set: make(map[string]bool)}
}

func (a *allowedIPSet) add(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.set[ip] = true
}

func (a *allowedIPSet) contains(ip string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.set[ip]
}

// ifReq is the ifreq structure for TUNSETIFF.
type ifReq struct {
	Name  [16]byte
	Flags uint16
	_     [22]byte
}

// openTUN opens /dev/net/tun and creates a TUN interface with the given name.
func openTUN(name string) (fd int, devName string, err error) {
	fd, err = unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return -1, "", fmt.Errorf("open /dev/net/tun: %w", err)
	}
	var req ifReq
	copy(req.Name[:], name)
	req.Flags = unix.IFF_TUN | unix.IFF_NO_PI
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(fd), unix.TUNSETIFF, uintptr(unsafe.Pointer(&req)))
	if errno != 0 {
		unix.Close(fd)
		return -1, "", fmt.Errorf("TUNSETIFF: %w", errno)
	}
	devName = strings.TrimRight(string(req.Name[:]), "\x00")
	return fd, devName, nil
}

func runCmd(prog string, args ...string) error {
	cmd := exec.Command(prog, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// configureIface brings up a network interface and assigns a CIDR address.
func configureIface(name, cidr string) error {
	if err := runCmd("ip", "link", "set", name, "up"); err != nil {
		return err
	}
	return runCmd("ip", "addr", "add", cidr, "dev", name)
}

// addDefaultRoute adds a default route via the given gateway IP.
func addDefaultRoute(via string) error {
	return runCmd("ip", "route", "add", "default", "via", via)
}

// sendFD sends a file descriptor over a Unix socket using SCM_RIGHTS.
func sendFD(conn *net.UnixConn, fd int) error {
	rights := syscall.UnixRights(fd)
	_, _, err := conn.WriteMsgUnix([]byte("fd"), rights, nil)
	return err
}

// recvFD receives a file descriptor over a Unix socket using SCM_RIGHTS.
func recvFD(conn *net.UnixConn) (int, error) {
	buf := make([]byte, 32)
	oob := make([]byte, syscall.CmsgSpace(4))
	_, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return -1, err
	}
	scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return -1, err
	}
	if len(scms) != 1 {
		return -1, fmt.Errorf("expected 1 scm, got %d", len(scms))
	}
	fds, err := syscall.ParseUnixRights(&scms[0])
	if err != nil {
		return -1, err
	}
	if len(fds) != 1 {
		return -1, fmt.Errorf("expected 1 fd, got %d", len(fds))
	}
	return fds[0], nil
}

// handleDNSConn handles a single DNS query/response cycle.
func handleDNSConn(conn *gonet.UDPConn, policy *Policy, allowedIPs *allowedIPSet) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gw/dns] read query: %v\n", err)
		return
	}
	payload := buf[:n]

	var p dnsmessage.Parser
	hdr, err := p.Start(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gw/dns] parse header: %v\n", err)
		return
	}
	questions, err := p.AllQuestions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gw/dns] parse questions: %v\n", err)
		return
	}

	rb := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               hdr.ID,
		Response:         true,
		Authoritative:    true,
		RecursionDesired: hdr.RecursionDesired,
	})
	rb.EnableCompression()
	if err := rb.StartQuestions(); err != nil {
		return
	}
	for _, q := range questions {
		rb.Question(q) //nolint:errcheck
	}

	var rcode dnsmessage.RCode
	var answeredIPs []string

	if err := rb.StartAnswers(); err != nil {
		return
	}

	resolver := policy.Resolve
	if resolver == nil {
		resolver = func(name string) ([]string, error) {
			return net.DefaultResolver.LookupHost(context.Background(), name)
		}
	}

	for _, q := range questions {
		name := q.Name.String()
		nameTrimmed := strings.ToLower(strings.TrimSuffix(name, "."))

		if q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA {
			continue
		}

		if !policy.Decide(nameTrimmed) {
			fmt.Fprintf(os.Stderr, "[gw/dns] REFUSED query for %s\n", nameTrimmed)
			rcode = dnsmessage.RCodeRefused
			continue
		}

		addrs, resolveErr := resolver(nameTrimmed)
		if resolveErr != nil {
			fmt.Fprintf(os.Stderr, "[gw/dns] resolve %s: %v\n", nameTrimmed, resolveErr)
			rcode = dnsmessage.RCodeNameError
			continue
		}

		if q.Type == dnsmessage.TypeA {
			for _, addr := range addrs {
				ip := net.ParseIP(addr).To4()
				if ip == nil {
					continue
				}
				var a [4]byte
				copy(a[:], ip)
				if err := rb.AResource(dnsmessage.ResourceHeader{
					Name:  q.Name,
					Type:  dnsmessage.TypeA,
					Class: dnsmessage.ClassINET,
					TTL:   60,
				}, dnsmessage.AResource{A: a}); err == nil {
					answeredIPs = append(answeredIPs, addr)
					fmt.Fprintf(os.Stderr, "[gw/dns] ALLOW %s → %s\n", nameTrimmed, addr)
				}
				break // return first IPv4 only
			}
		}
	}

	msg, err := rb.Finish()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gw/dns] builder Finish: %v\n", err)
		return
	}

	if len(msg) >= 4 && rcode != dnsmessage.RCodeSuccess {
		msg[3] = (msg[3] & 0xF0) | byte(rcode)
	}

	if rcode == dnsmessage.RCodeSuccess {
		for _, ip := range answeredIPs {
			allowedIPs.add(ip)
		}
	}

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, wErr := conn.Write(msg); wErr != nil {
		fmt.Fprintf(os.Stderr, "[gw/dns] write response: %v\n", wErr)
	}
}

// splice copies data bidirectionally between left (gVisor conn) and a real TCP dst.
func splice(left net.Conn, dst string) {
	defer left.Close()
	right, err := net.DialTimeout("tcp", dst, 3*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gw/tcp] dial %s: %v\n", dst, err)
		return
	}
	defer right.Close()
	done := make(chan struct{}, 2)
	cp := func(a, b net.Conn) {
		io.Copy(a, b)
		done <- struct{}{}
	}
	go cp(left, right)
	go cp(right, left)
	<-done
}

// buildStack creates a gVisor network stack on the given TUN fd, with DNS and TCP forwarders.
func buildStack(tunFd int, gwIP string, policy *Policy, allowedIPs *allowedIPSet) (*stack.Stack, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	ep, err := fdbased.New(&fdbased.Options{
		FDs:            []int{tunFd},
		MTU:            tunMTU,
		EthernetHeader: false,
	})
	if err != nil {
		return nil, fmt.Errorf("fdbased.New: %v", err)
	}

	if tcpErr := s.CreateNIC(nicID, ep); tcpErr != nil {
		return nil, fmt.Errorf("CreateNIC: %v", tcpErr)
	}

	addr := tcpip.AddrFromSlice(net.ParseIP(gwIP).To4())
	tcpErr := s.AddProtocolAddress(nicID, tcpip.ProtocolAddress{
		Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   addr,
			PrefixLen: 24,
		},
	}, stack.AddressProperties{})
	if tcpErr != nil {
		return nil, fmt.Errorf("AddProtocolAddress: %v", tcpErr)
	}

	s.SetRouteTable([]tcpip.Route{{
		Destination: header.IPv4EmptySubnet,
		NIC:         nicID,
	}})

	if tcpErr := s.SetPromiscuousMode(nicID, true); tcpErr != nil {
		return nil, fmt.Errorf("SetPromiscuousMode: %v", tcpErr)
	}

	if tcpErr := s.SetSpoofing(nicID, true); tcpErr != nil {
		return nil, fmt.Errorf("SetSpoofing: %v", tcpErr)
	}

	fwd := tcp.NewForwarder(s, 0, 16, func(req *tcp.ForwarderRequest) {
		id := req.ID()
		destIP := id.LocalAddress.String()
		destPort := id.LocalPort
		key := fmt.Sprintf("%s:%d", destIP, destPort)

		if !allowedIPs.contains(destIP) {
			fmt.Fprintf(os.Stderr, "[gw/tcp] RST %s (not in allowed-IP set)\n", key)
			req.Complete(true)
			return
		}

		realDst, ok := policy.TCPRedirect[key]
		if !ok {
			// IP is allowed but no redirect — dial real destination directly.
			realDst = key
		}

		var wq waiter.Queue
		ep, tcpErr := req.CreateEndpoint(&wq)
		if tcpErr != nil {
			fmt.Fprintf(os.Stderr, "[gw/tcp] CreateEndpoint %s: %v\n", key, tcpErr)
			req.Complete(true)
			return
		}
		req.Complete(false)
		conn := gonet.NewTCPConn(&wq, ep)
		fmt.Fprintf(os.Stderr, "[gw/tcp] forwarding %s → %s\n", key, realDst)
		go splice(conn, realDst)
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)

	udpFwd := udp.NewForwarder(s, func(req *udp.ForwarderRequest) bool {
		id := req.ID()
		if id.LocalPort != 53 {
			return true
		}

		var wq waiter.Queue
		ep, tcpErr := req.CreateEndpoint(&wq)
		if tcpErr != nil {
			fmt.Fprintf(os.Stderr, "[gw/dns] CreateEndpoint: %v\n", tcpErr)
			return true
		}
		conn := gonet.NewUDPConn(&wq, ep)
		go handleDNSConn(conn, policy, allowedIPs)
		return true
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	return s, nil
}

// Init must be called at the very start of main() before any other initialization.
// If this process was re-exec'd as a gateway child (NETGW_MODE=child is set),
// Init configures the network namespace and execs the confined argv, then exits.
// Otherwise Init returns immediately and the caller's main continues.
func Init() {
	if os.Getenv("NETGW_MODE") != "child" {
		return
	}

	fmt.Fprintln(os.Stderr, "[child] starting in new netns")

	// Get the socket fd used for fd-passing with the parent.
	sockFdStr := os.Getenv("NETGW_SOCKFD")
	if sockFdStr == "" {
		fmt.Fprintln(os.Stderr, "[child] NETGW_SOCKFD not set")
		os.Exit(1)
	}
	sockFdNum, err := strconv.Atoi(sockFdStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[child] invalid NETGW_SOCKFD: %v\n", err)
		os.Exit(1)
	}

	sockFile := os.NewFile(uintptr(sockFdNum), "child-sock")
	conn, err := net.FileConn(sockFile)
	sockFile.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[child] FileConn: %v\n", err)
		os.Exit(1)
	}
	unixConn := conn.(*net.UnixConn)

	// Create TUN inside this new net namespace.
	tunFd, tunName, err := openTUN("tun0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[child] openTUN: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[child] created TUN %s (fd %d)\n", tunName, tunFd)

	if err := configureIface(tunName, childIP+"/24"); err != nil {
		fmt.Fprintf(os.Stderr, "[child] configureIface: %v\n", err)
		os.Exit(1)
	}
	if err := addDefaultRoute(gatewayIP); err != nil {
		fmt.Fprintf(os.Stderr, "[child] addDefaultRoute: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "[child] TUN configured, sending fd to gateway")

	if err := sendFD(unixConn, tunFd); err != nil {
		fmt.Fprintf(os.Stderr, "[child] sendFD: %v\n", err)
		os.Exit(1)
	}

	// Wait for "ready" signal from parent.
	unixConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	ready := make([]byte, 1)
	if _, err := unixConn.Read(ready); err != nil {
		fmt.Fprintf(os.Stderr, "[child] wait ready: %v\n", err)
		os.Exit(1)
	}
	unixConn.Close()
	fmt.Fprintln(os.Stderr, "[child] gateway ready, exec-ing confined argv")

	time.Sleep(200 * time.Millisecond) // let gVisor settle

	// Decode and exec the confined argv.
	argvJSON := os.Getenv("NETGW_CHILD_ARGV")
	if argvJSON == "" {
		fmt.Fprintln(os.Stderr, "[child] NETGW_CHILD_ARGV not set")
		os.Exit(1)
	}
	var argv []string
	if err := json.Unmarshal([]byte(argvJSON), &argv); err != nil {
		fmt.Fprintf(os.Stderr, "[child] decode NETGW_CHILD_ARGV: %v\n", err)
		os.Exit(1)
	}
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "[child] empty argv")
		os.Exit(1)
	}

	// Build clean env: caller's env minus the internal NETGW_ setup vars.
	// User-supplied env vars (even NETGW_-prefixed) that were passed in
	// via cmd.Env are preserved; only the three bootstrap vars are dropped.
	internalVars := map[string]bool{
		"NETGW_MODE":       true,
		"NETGW_SOCKFD":     true,
		"NETGW_CHILD_ARGV": true,
		"NETGW_CHILD_DIR":  true,
	}
	var cleanEnv []string
	for _, kv := range os.Environ() {
		key := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			key = kv[:idx]
		}
		if !internalVars[key] {
			cleanEnv = append(cleanEnv, kv)
		}
	}

	// Change to the requested working directory before exec.
	if dir := os.Getenv("NETGW_CHILD_DIR"); dir != "" {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintf(os.Stderr, "[child] chdir %q: %v\n", dir, err)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "[child] execing %v\n", argv)

	// Apply any additional confinement (e.g. Landlock FS) before exec.
	// This is the composition point: the hook is set by the host binary's
	// main() before Init() can run; the child inherits it and fires it here
	// after the netns+TUN wall is in place. Fail closed on any error.
	if PreExecHook != nil {
		if hookErr := PreExecHook(); hookErr != nil {
			fmt.Fprintf(os.Stderr, "egress: PreExecHook failed: %v\n", hookErr)
			os.Exit(1)
		}
	}

	if err := syscall.Exec(argv[0], argv, cleanEnv); err != nil {
		fmt.Fprintf(os.Stderr, "[child] exec %q: %v\n", argv[0], err)
		os.Exit(1)
	}
	// unreachable
}

// RunConfinedNetns sets up a network namespace with a gVisor egress gateway
// enforcing policy, then executes argv inside that confined namespace.
// Returns the child process's exit code.
//
// Init() must be called at the top of main() for this to work correctly —
// the child process is a re-exec of /proc/self/exe.
func RunConfinedNetns(policy Policy, argv []string, env []string, dir string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("RunConfinedNetns: empty argv")
	}

	// Encode argv for the child via environment variable.
	argvJSON, err := json.Marshal(argv)
	if err != nil {
		return 0, fmt.Errorf("RunConfinedNetns: encode argv: %w", err)
	}

	// Create a Unix socketpair for fd-passing between parent and child.
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return 0, fmt.Errorf("RunConfinedNetns: socketpair: %w", err)
	}
	parentSockFd, childSockFd := fds[0], fds[1]

	// Re-exec self via unshare --user --map-root-user --net.
	selfExe, err := os.Readlink("/proc/self/exe")
	if err != nil {
		selfExe = os.Args[0]
	}

	childEnv := make([]string, 0, len(env)+4)
	childEnv = append(childEnv, env...)
	childEnv = append(childEnv,
		"NETGW_MODE=child",
		"NETGW_SOCKFD=3",
		"NETGW_CHILD_ARGV="+string(argvJSON),
	)
	if dir != "" {
		childEnv = append(childEnv, "NETGW_CHILD_DIR="+dir)
	}

	cmd := exec.Command("unshare", "--user", "--map-root-user", "--net", selfExe)
	cmd.Env = childEnv
	childSockFile := os.NewFile(uintptr(childSockFd), "child-sock")
	cmd.ExtraFiles = []*os.File{childSockFile}
	// Pass the REAL stdin through the whole chain: unshare → selfExe (netns
	// child) → the final syscall.Exec'd target. Without this, fd 0 defaults to
	// /dev/null and the exec'd agent sees a closed stdin (isatty(0) == false),
	// so an interactive TUI agent (e.g. claude) cannot read the keyboard. The
	// inner re-exec inherits fds 0/1/2, so setting fd 0 here is sufficient for
	// the agent to receive keystrokes and for isatty(stdin) to be true when the
	// host was launched from a real terminal.
	//
	// This is stdin passthrough ONLY — it does not touch the network namespace,
	// TUN, gVisor stack, or DNS gateway, so the malicious-proof egress wall is
	// behaviourally unchanged.
	//
	// TODO: controlling TTY for signals — give the agent a controlling terminal
	// + foreground process group (SysProcAttr Setsid/Setctty/Foreground) so
	// Ctrl-C and SIGWINCH (window resize) reach it directly. Deferred because it
	// risks fighting the unshare user/net-namespace setup; the current bar is
	// "keyboard works + isatty true," which cmd.Stdin = os.Stdin meets.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		childSockFile.Close()
		unix.Close(parentSockFd)
		return 0, fmt.Errorf("RunConfinedNetns: start unshare: %w", err)
	}
	childSockFile.Close() // parent doesn't need the child end

	// Receive the TUN fd from the child.
	parentSockFile := os.NewFile(uintptr(parentSockFd), "parent-sock")
	parentConn, err := net.FileConn(parentSockFile)
	parentSockFile.Close()
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return 0, fmt.Errorf("RunConfinedNetns: parent FileConn: %w", err)
	}
	unixConn := parentConn.(*net.UnixConn)
	unixConn.SetReadDeadline(time.Now().Add(15 * time.Second))

	fmt.Fprintln(os.Stderr, "[gw] waiting for TUN fd from child...")
	tunFd, err := recvFD(unixConn)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return 0, fmt.Errorf("RunConfinedNetns: recvFD: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[gw] received TUN fd %d from child\n", tunFd)
	unixConn.SetReadDeadline(time.Time{})

	// Build gVisor stack on the child's TUN fd.
	allowedIPs := newAllowedIPSet()
	// Pre-approve any explicitly allowed IPs from policy.
	for _, ip := range policy.AllowIPs {
		allowedIPs.add(ip)
	}

	if _, err := buildStack(tunFd, gatewayIP, &policy, allowedIPs); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return 0, fmt.Errorf("RunConfinedNetns: buildStack: %w", err)
	}
	fmt.Fprintln(os.Stderr, "[gw] gVisor stack running, signalling child")

	// Signal child that the gateway is ready.
	if _, writeErr := unixConn.Write([]byte{1}); writeErr != nil {
		fmt.Fprintf(os.Stderr, "[gw] write ready signal: %v\n", writeErr)
	}

	// Wait for the child to exit.
	waitErr := cmd.Wait()
	if waitErr != nil {
		if ex, ok := waitErr.(*exec.ExitError); ok {
			return ex.ExitCode(), nil
		}
		return 0, fmt.Errorf("RunConfinedNetns: wait: %w", waitErr)
	}
	return 0, nil
}
