// Command fakeproxy stands in for the real proxy command in the integration
// tests. It never touches a network origin. Behaviour is chosen by argv[1];
// FP_PIDFILE, FP_MARKER, FP_PORTFILE and FP_TTYFILE name the files it writes.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func writeFile(env, content string) {
	if p := os.Getenv(env); p != "" {
		_ = os.WriteFile(p, []byte(content), 0o600)
	}
}

func appendLine(env, line string) {
	p := os.Getenv(env)
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, line)
	_ = f.Close()
}

func main() {
	mode := "exit"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode != "listen" {
		writeFile("FP_PIDFILE", strconv.Itoa(os.Getpid()))
	}
	switch mode {
	case "exit":
		appendLine("FP_MARKER", "ran")
		code := 0
		if len(os.Args) > 2 {
			code, _ = strconv.Atoi(os.Args[2])
		}
		os.Exit(code)
	case "sleep":
		appendLine("FP_MARKER", "ran")
		time.Sleep(5 * time.Minute)
	case "marker":
		appendLine("FP_MARKER", "ran")
		time.Sleep(500 * time.Millisecond)
		os.Exit(1)
	case "cat":
		appendLine("FP_MARKER", "ran")
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "tty":
		appendLine("FP_MARKER", "ran")
		self, _ := syscall.Getpgid(0)
		parent, _ := syscall.Getpgid(os.Getppid())
		writeFile("FP_TTYFILE", fmt.Sprintf("%d %d", self, parent))
		if f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
			_, _ = f.WriteString("from-child\n")
			_ = f.Close()
		}
	case "listener":
		appendLine("FP_MARKER", "ran")
		c := exec.Command(os.Args[0], "listen")
		c.Env = os.Environ()
		_ = c.Start()
		time.Sleep(5 * time.Minute)
	case "listen":
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		writeFile("FP_PORTFILE", strconv.Itoa(l.Addr().(*net.TCPAddr).Port))
		time.Sleep(5 * time.Minute)
	}
}
