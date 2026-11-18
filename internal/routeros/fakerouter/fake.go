// Package fakerouter is an in-process fake MikroTik RouterOS API server for tests.
package fakerouter

import (
	"bufio"
	"net"
	"strings"
	"sync"

	"github.com/mmdelhajj/paxve/internal/routeros"
)

type User struct {
	ID       string
	Name     string
	Disabled bool
}

// Router keeps PPPoE secrets and active sessions in memory and records every command.
type Router struct {
	User, Password string

	mu       sync.Mutex
	secrets  map[string]*User
	active   map[string]string // session id -> user name
	Commands []string
	ln       net.Listener
}

// Start listens on 127.0.0.1 on a free port.
func Start(user, password string) (*Router, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &Router{User: user, Password: password, secrets: map[string]*User{}, active: map[string]string{}, ln: ln}
	go r.serve()
	return r, nil
}

func (r *Router) Port() int    { return r.ln.Addr().(*net.TCPAddr).Port }
func (r *Router) Close() error { return r.ln.Close() }

// AddSecret creates a PPPoE secret with an active session.
func (r *Router) AddSecret(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := "*" + string(rune('A'+len(r.secrets)))
	r.secrets[name] = &User{ID: id, Name: name}
	r.active["*S"+id[1:]] = name
}

func (r *Router) Disabled(name string) (disabled, exists bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.secrets[name]
	if !ok {
		return false, false
	}
	return u.Disabled, true
}

func (r *Router) ActiveSessions(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, u := range r.active {
		if u == name {
			n++
		}
	}
	return n
}

func (r *Router) Log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.Commands...)
}

func (r *Router) serve() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.handle(c)
	}
}

func (r *Router) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	logged := false
	send := func(words ...string) { _, _ = c.Write(routeros.EncodeSentence(words...)) }
	for {
		s, err := routeros.ReadSentence(br)
		if err != nil || len(s) == 0 {
			return
		}
		cmd := s[0]
		a := routeros.Attrs(s[1:])
		q := map[string]string{}
		for _, w := range s[1:] {
			if strings.HasPrefix(w, "?") {
				if k, v, ok := strings.Cut(w[1:], "="); ok {
					q[k] = v
				}
			}
		}
		r.mu.Lock()
		r.Commands = append(r.Commands, strings.Join(s, " "))
		r.mu.Unlock()
		if cmd != "/login" && !logged {
			send("!fatal", "not logged in")
			return
		}
		switch cmd {
		case "/login":
			if a["name"] == r.User && a["password"] == r.Password {
				logged = true
				send("!done")
			} else {
				send("!trap", "=message=invalid user name or password (6)")
				send("!done")
			}
		case "/system/identity/print":
			send("!re", "=name=FakeRouter")
			send("!done")
		case "/system/resource/print":
			send("!re", "=version=7.16 (stable)")
			send("!done")
		case "/ppp/secret/print":
			r.mu.Lock()
			u := r.secrets[q["name"]]
			r.mu.Unlock()
			if u != nil {
				d := "false"
				if u.Disabled {
					d = "true"
				}
				send("!re", "=.id="+u.ID, "=name="+u.Name, "=disabled="+d)
			}
			send("!done")
		case "/ppp/secret/set":
			r.mu.Lock()
			found := false
			for _, u := range r.secrets {
				if u.ID == a[".id"] {
					u.Disabled = a["disabled"] == "yes"
					found = true
				}
			}
			r.mu.Unlock()
			if !found {
				send("!trap", "=message=no such item")
			}
			send("!done")
		case "/ppp/active/print":
			r.mu.Lock()
			var ids []string
			for id, u := range r.active {
				if u == q["name"] {
					ids = append(ids, id)
				}
			}
			r.mu.Unlock()
			for _, id := range ids {
				send("!re", "=.id="+id)
			}
			send("!done")
		case "/ppp/active/remove":
			r.mu.Lock()
			delete(r.active, a[".id"])
			r.mu.Unlock()
			send("!done")
		default:
			send("!trap", "=message=no such command")
			send("!done")
		}
	}
}
