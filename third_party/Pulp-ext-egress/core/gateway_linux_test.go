//go:build linux

package core

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// TestMain calls Init() first so that when the test binary is re-exec'd as a
// gateway child, Init() handles the child logic and exits before any test runs.
func TestMain(m *testing.M) {
	Init()
	os.Exit(m.Run())
}

// TestConfinedNetns is the outer test. It starts a local TCP echo server,
// sets up a confined-netns policy, and runs the child probes inside the netns.
func TestConfinedNetns(t *testing.T) {
	if os.Getenv("NETGW_CONFINED") == "1" {
		t.Skip("this test must not run inside the confined netns")
	}

	// Start a TCP echo server that sends a fixed reply.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	echoAddr := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				fmt.Fprint(c, "HELLO_FROM_RESOLVED\n")
			}(c)
		}
	}()

	const resolvedTestIP = "10.0.0.50"
	policy := Policy{
		AllowNames: []string{"allowed.test", ".allowed.test"},
		Resolve: func(name string) ([]string, error) {
			if name == "allowed.test" {
				return []string{resolvedTestIP}, nil
			}
			return nil, fmt.Errorf("no such host: %s", name)
		},
		TCPRedirect: map[string]string{
			resolvedTestIP + ":80": echoAddr,
		},
	}

	selfExe, err := os.Readlink("/proc/self/exe")
	if err != nil {
		selfExe = os.Args[0]
	}
	argv := []string{
		selfExe,
		"-test.run=TestConfinedChildProbes",
		"-test.v",
	}
	env := append(os.Environ(),
		"NETGW_CONFINED=1",
		"NETGW_ECHO_ADDR="+echoAddr,
	)

	exitCode, err := RunConfinedNetns(policy, argv, env, "")
	if err != nil {
		t.Fatalf("RunConfinedNetns: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("confined child exited with code %d", exitCode)
	}
}

// TestConfinedChildProbes runs INSIDE the confined network namespace.
// It is skipped when not running in confined mode (NETGW_CONFINED != "1").
// It performs 5 probes and prints the required markers.
func TestConfinedChildProbes(t *testing.T) {
	if os.Getenv("NETGW_CONFINED") != "1" {
		t.Skip("not in confined netns")
	}

	dnsServer := gatewayIP + ":53"
	const resolvedTestIP = "10.0.0.50"

	// ── Probe 1: RESOLVE_ALLOWED ──────────────────────────────────────────────
	t.Run("probe1_resolve_allowed", func(t *testing.T) {
		resp, err := sendDNSQueryTest("allowed.test", dnsServer)
		if err != nil {
			t.Logf("RESOLVE_ALLOWED:FAIL:%v", err)
			t.Fatalf("DNS query failed: %v", err)
		}
		rcode, ip, parseErr := parseDNSResponseTest(resp)
		if parseErr != nil {
			t.Logf("RESOLVE_ALLOWED:FAIL:parse:%v", parseErr)
			t.Fatalf("parse failed: %v", parseErr)
		}
		if rcode != dnsmessage.RCodeSuccess || ip == "" {
			t.Logf("RESOLVE_ALLOWED:FAIL:rcode=%v ip=%s", rcode, ip)
			t.Fatalf("unexpected rcode=%v ip=%s", rcode, ip)
		}
		t.Logf("RESOLVE_ALLOWED:OK")
		fmt.Println("RESOLVE_ALLOWED:OK")
	})

	// Give gateway a moment to add IP to allowed set.
	time.Sleep(150 * time.Millisecond)

	// ── Probe 2: CONNECT_RESOLVED ─────────────────────────────────────────────
	t.Run("probe2_connect_resolved", func(t *testing.T) {
		target := resolvedTestIP + ":80"
		c, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err != nil {
			t.Logf("CONNECT_RESOLVED:FAIL:%v", err)
			t.Fatalf("dial %s: %v", target, err)
		}
		buf := make([]byte, 64)
		c.SetReadDeadline(time.Now().Add(4 * time.Second))
		n, _ := c.Read(buf)
		c.Close()
		reply := strings.TrimSpace(string(buf[:n]))
		if !strings.Contains(reply, "HELLO_FROM_RESOLVED") {
			t.Logf("CONNECT_RESOLVED:FAIL:wrong reply: %q", reply)
			t.Fatalf("unexpected reply: %q", reply)
		}
		t.Logf("CONNECT_RESOLVED:OK")
		fmt.Println("CONNECT_RESOLVED:OK")
	})

	// ── Probe 3: RESOLVE_BLOCKED ──────────────────────────────────────────────
	t.Run("probe3_resolve_blocked", func(t *testing.T) {
		resp, err := sendDNSQueryTest("blocked.test", dnsServer)
		if err != nil {
			// No response = blocked, that's fine.
			t.Logf("RESOLVE_BLOCKED:DENIED:%v", err)
			fmt.Println("RESOLVE_BLOCKED:DENIED")
			return
		}
		rcode, _, _ := parseDNSResponseTest(resp)
		if rcode == dnsmessage.RCodeRefused || rcode == dnsmessage.RCodeNameError {
			t.Logf("RESOLVE_BLOCKED:DENIED:rcode=%v", rcode)
			fmt.Println("RESOLVE_BLOCKED:DENIED")
		} else {
			t.Logf("RESOLVE_BLOCKED:REACHED:rcode=%v", rcode)
			t.Fatalf("expected REFUSED/NXDOMAIN, got rcode=%v", rcode)
		}
	})

	// ── Probe 4: RAWIP_BLOCKED ────────────────────────────────────────────────
	t.Run("probe4_rawip_blocked", func(t *testing.T) {
		c, err := net.DialTimeout("tcp", "10.0.0.77:80", 2*time.Second)
		if err != nil {
			t.Logf("RAWIP_BLOCKED:DENIED:%v", err)
			fmt.Println("RAWIP_BLOCKED:DENIED")
			return
		}
		c.Close()
		t.Logf("RAWIP_BLOCKED:REACHED")
		fmt.Println("RAWIP_BLOCKED:REACHED")
		t.Fatalf("expected connection to be blocked")
	})

	// ── Probe 5: BYPASS ───────────────────────────────────────────────────────
	t.Run("probe5_bypass", func(t *testing.T) {
		c, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second)
		if err != nil {
			t.Logf("BYPASS:BLOCKED:%v", err)
			fmt.Println("BYPASS:BLOCKED")
			return
		}
		c.Close()
		t.Logf("BYPASS:REACHED")
		fmt.Println("BYPASS:REACHED")
		t.Fatalf("expected bypass to be blocked")
	})
}

// sendDNSQueryTest sends a DNS A query to dstAddr and returns the raw response bytes.
func sendDNSQueryTest(name, dstAddr string) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               0x1337,
		RecursionDesired: true,
	})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	dnsName, err := dnsmessage.NewName(name + ".")
	if err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsName,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	msg, err := b.Finish()
	if err != nil {
		return nil, err
	}

	conn, err := net.DialTimeout("udp", dstAddr, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial UDP %s: %w", dstAddr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		return nil, fmt.Errorf("write DNS query: %w", err)
	}
	resp := make([]byte, 512)
	n, err := conn.Read(resp)
	if err != nil {
		return nil, fmt.Errorf("read DNS response: %w", err)
	}
	return resp[:n], nil
}

// parseDNSResponseTest extracts the RCODE and first A-record IP from a DNS response.
func parseDNSResponseTest(resp []byte) (rcode dnsmessage.RCode, ip string, err error) {
	if len(resp) < 4 {
		return 0, "", fmt.Errorf("response too short")
	}
	rcode = dnsmessage.RCode(resp[3] & 0x0F)
	ancount := binary.BigEndian.Uint16(resp[6:8])
	if ancount == 0 || rcode != dnsmessage.RCodeSuccess {
		return rcode, "", nil
	}
	var p dnsmessage.Parser
	if _, err := p.Start(resp); err != nil {
		return rcode, "", err
	}
	if err := p.SkipAllQuestions(); err != nil {
		return rcode, "", err
	}
	for {
		ah, err := p.AnswerHeader()
		if err == dnsmessage.ErrSectionDone {
			break
		}
		if err != nil {
			return rcode, "", err
		}
		if ah.Type == dnsmessage.TypeA {
			r, err := p.AResource()
			if err != nil {
				return rcode, "", err
			}
			ip = fmt.Sprintf("%d.%d.%d.%d", r.A[0], r.A[1], r.A[2], r.A[3])
			return rcode, ip, nil
		}
		if err := p.SkipAnswer(); err != nil {
			break
		}
	}
	return rcode, "", nil
}
