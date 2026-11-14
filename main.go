// work — the front door to your dev box.
//
//	work login <email>    sign in (your own Google account, via Jumpstart Lab SSO)
//	work                  open your box (if you have exactly one)
//	work <box>            wake it if it is asleep, then attach its `work` tmux session
//	work list             your boxes and whether each is running
//	work code <box> [dir] open VS Code on this machine, Remote-SSH'd into the box
//	work doctor           check sign-in, Switchboard, tailnet, and et/ssh
//	work logout           forget the sign-in
//
// Boxes live on your own tailnet; Switchboard (boxes.mine / status / wake) says
// which are yours and starts one that the idle reaper has suspended.
package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "work:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "login":
		email := ""
		if len(args) > 1 {
			email = args[1]
		}
		return login(email)
	case "logout":
		return logout()
	case "list", "ls":
		return list()
	case "code":
		if len(args) < 2 {
			return errors.New("usage: work code <box> [dir]")
		}
		dir := "/workspace"
		if len(args) > 2 {
			dir = args[2]
		}
		return code(args[1], dir)
	case "doctor":
		return doctor()
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "":
		boxes, err := myBoxes()
		if err != nil {
			return err
		}
		switch len(boxes) {
		case 0:
			return errors.New("you have no boxes yet — ask Jeff")
		case 1:
			return open(boxes[0].Name)
		default:
			return errors.New("you have several boxes — `work list`, then `work <box>`")
		}
	default:
		return open(cmd)
	}
}

const usage = `usage: work [<box>]           open your box (wakes it if asleep)
       work login <email>     sign in with your own work email
       work list              your boxes and their state
       work code <box> [dir]  VS Code here, Remote-SSH'd into the box
       work doctor            check the whole path
       work logout            forget the sign-in
`

func list() error {
	boxes, err := myBoxes()
	if err != nil {
		return err
	}
	if len(boxes) == 0 {
		fmt.Println("no boxes")
		return nil
	}
	for _, b := range boxes {
		state := "?"
		if s, err := status(b.Name); err == nil {
			state = s.State
			if s.Health != "" && s.Health != "none" {
				state += " (" + s.Health + ")"
			}
		} else {
			state = "unknown: " + err.Error()
		}
		fmt.Printf("%-16s %-24s %s@%s\n", b.Name, state, b.Connect.User, b.Connect.Host)
	}
	return nil
}

// ready wakes the box if needed and waits until its et/ssh port answers on
// the tailnet, returning where to connect.
func ready(name string) (connect, error) {
	s, err := wake(name)
	if err != nil {
		return connect{}, err
	}
	c := s.Connect
	if c.Host == "" {
		return c, fmt.Errorf("switchboard gave no connect host for %s", name)
	}
	deadline := time.Now().Add(90 * time.Second)
	said := false
	for {
		if reachable(c.Host, "2022") || reachable(c.Host, "22") {
			return c, nil
		}
		if time.Now().After(deadline) {
			return c, fmt.Errorf("%s is %s but %s is not answering on your tailnet — is Tailscale up on this machine? (`work doctor`)", name, s.State, c.Host)
		}
		if !said {
			fmt.Fprintf(os.Stderr, "waking %s…\n", name)
			said = true
		}
		time.Sleep(3 * time.Second)
	}
}

func open(name string) error {
	c, err := ready(name)
	if err != nil {
		return err
	}
	target := c.User + "@" + c.Host
	attach := "tmux new-session -A -s work"
	if p, err := exec.LookPath("et"); err == nil && reachable(c.Host, "2022") {
		return execve(p, []string{"et", target, "-c", attach})
	}
	p, err := exec.LookPath("ssh")
	if err != nil {
		return errors.New("neither et nor ssh is installed")
	}
	return execve(p, []string{"ssh", "-t", target, attach})
}

func code(name, dir string) error {
	c, err := ready(name)
	if err != nil {
		return err
	}
	p, err := exec.LookPath("code")
	if err != nil {
		return errors.New("VS Code's `code` command is not on PATH (VS Code → Command Palette → \"Shell Command: Install 'code' command\")")
	}
	return exec.Command(p, "--remote", "ssh-remote+"+c.User+"@"+c.Host, dir).Run()
}

func doctor() error {
	ok := true
	check := func(name string, err error, fix string) {
		if err == nil {
			fmt.Printf("  ok    %s\n", name)
			return
		}
		ok = false
		fmt.Printf("  FAIL  %s: %v\n        → %s\n", name, err, fix)
	}
	_, err := accessToken()
	check("signed in", err, "work login <your email>")
	if err == nil {
		who, _ := whoami()
		fmt.Printf("        as %s\n", who)
	}
	boxes, err := myBoxes()
	check("switchboard answers", err, "check your network; if it says refused, `work login`")
	_, err = exec.LookPath("tailscale")
	if err != nil {
		_, err2 := os.Stat("/Applications/Tailscale.app")
		if err2 == nil {
			err = nil
		}
	}
	check("tailscale installed", err, "install Tailscale and sign in to your own tailnet")
	_, etErr := exec.LookPath("et")
	check("et installed (optional; ssh is the fallback)", etErr, "brew install MisterTea/et/et")
	for _, b := range boxes {
		s, err := status(b.Name)
		check("box "+b.Name+" known to switchboard", err, "ask Jeff")
		if err == nil {
			fmt.Printf("        %s (%s)\n", s.State, s.Health)
			if s.State == "running" {
				var e error
				if !reachable(b.Connect.Host, "22") {
					e = fmt.Errorf("%s:22 unreachable", b.Connect.Host)
				}
				check("box "+b.Name+" reachable on your tailnet", e, "is Tailscale connected? is "+b.Connect.Host+" in your tailnet's machine list?")
			}
		}
	}
	if !ok {
		return errors.New("some checks failed")
	}
	return nil
}

func reachable(host, port string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func execve(path string, argv []string) error {
	return syscall.Exec(path, argv, os.Environ())
}

func openBrowser(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", u)
	case "linux":
		c = exec.Command("xdg-open", u)
	default:
		return
	}
	_ = c.Start()
}

func decodeSegment(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}
