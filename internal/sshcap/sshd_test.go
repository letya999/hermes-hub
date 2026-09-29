package sshcap

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sshd is a minimal in-process SSH server for exercising the capability
// end to end: pubkey auth, pinned host keys, exec, sftp, echo-shell PTYs
// and direct-tcpip forwarding.
type sshd struct {
	addr    string
	hostKey ssh.PublicKey
	ln      net.Listener
	wg      sync.WaitGroup
}

type sshdOpts struct {
	authKey     ssh.PublicKey
	caKey       ssh.PublicKey // non-nil accepts user certificates signed by this CA
	exec        func(cmd string) (stdout, stderr string, code int)
	fs          *memFS // non-nil enables the sftp subsystem
	shell       bool
	directTCPIP bool
}

// memFS is a shared in-memory SFTP backend: one map backs every connection so
// writes persist across the per-call dials the service performs.
type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMemFS() *memFS { return &memFS{files: map[string][]byte{}} }

func (m *memFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, ok := m.files[r.Filepath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return bytes.NewReader(body), nil
}

type memWriter struct {
	fs   *memFS
	path string
	buf  []byte
	done bool
}

func (w *memWriter) WriteAt(p []byte, off int64) (int, error) {
	if w.done {
		return 0, os.ErrClosed
	}
	if end := int(off) + len(p); end > len(w.buf) {
		next := make([]byte, end)
		copy(next, w.buf)
		w.buf = next
	}
	return copy(w.buf[off:], p), nil
}

func (w *memWriter) Close() error {
	w.fs.mu.Lock()
	w.fs.files[w.path] = w.buf
	w.fs.mu.Unlock()
	w.done = true
	return nil
}

func (m *memFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	return &memWriter{fs: m, path: r.Filepath}, nil
}

func (m *memFS) Filecmd(r *sftp.Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.Method {
	case "Rename":
		body, ok := m.files[r.Filepath]
		if !ok {
			return os.ErrNotExist
		}
		m.files[r.Target] = body
		delete(m.files, r.Filepath)
	case "Remove":
		if _, ok := m.files[r.Filepath]; !ok {
			return os.ErrNotExist
		}
		delete(m.files, r.Filepath)
	case "Mkdir", "Rmdir", "Setstat":
	default:
		return fmt.Errorf("unsupported sftp command %q", r.Method)
	}
	return nil
}

func (m *memFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	return nil, fmt.Errorf("listing unsupported")
}

func startSSHD(t *testing.T, opts sshdOpts) *sshd {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if opts.authKey == nil || string(key.Marshal()) == string(opts.authKey.Marshal()) {
				return nil, nil
			}
			if cert, ok := key.(*ssh.Certificate); ok && opts.caKey != nil && string(cert.SignatureKey.Marshal()) == string(opts.caKey.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &sshd{addr: ln.Addr().String(), hostKey: signer.PublicKey(), ln: ln}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				d.serve(c, cfg, opts)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		d.wg.Wait()
	})
	return d
}

func (d *sshd) serve(c net.Conn, cfg *ssh.ServerConfig, opts sshdOpts) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		switch ch.ChannelType() {
		case "session":
			go d.session(ch, opts)
		case "direct-tcpip":
			go d.directTCPIP(ch, opts)
		default:
			_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

type execRequest struct{ Command string }
type subsystemRequest struct{ Name string }
type tcpipForward struct {
	DestHost string
	DestPort uint32
	SrcHost  string
	SrcPort  uint32
}

func (d *sshd) session(ch ssh.NewChannel, opts sshdOpts) {
	channel, reqs, err := ch.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var r execRequest
			if err := ssh.Unmarshal(req.Payload, &r); err != nil || opts.exec == nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			out, errOut, code := opts.exec(r.Command)
			_, _ = channel.Write([]byte(out))
			_, _ = channel.Stderr().Write([]byte(errOut))
			_, _ = channel.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, uint32(code)))
			return
		case "pty-req":
			_ = req.Reply(true, nil)
		case "env", "window-change":
			_ = req.Reply(true, nil)
		case "shell":
			if !opts.shell {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			_, _ = io.Copy(channel, channel) // echo until close
			return
		case "subsystem":
			var r subsystemRequest
			if err := ssh.Unmarshal(req.Payload, &r); err != nil || r.Name != "sftp" || opts.fs == nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			srv := sftp.NewRequestServer(channel, sftp.Handlers{FileGet: opts.fs, FilePut: opts.fs, FileCmd: opts.fs, FileList: opts.fs})
			_ = srv.Serve()
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (d *sshd) directTCPIP(ch ssh.NewChannel, opts sshdOpts) {
	if !opts.directTCPIP {
		_ = ch.Reject(ssh.Prohibited, "forwarding disabled")
		return
	}
	var r tcpipForward
	if err := ssh.Unmarshal(ch.ExtraData(), &r); err != nil {
		_ = ch.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	target, err := net.DialTimeout("tcp", net.JoinHostPort(r.DestHost, fmt.Sprint(r.DestPort)), 5*time.Second)
	if err != nil {
		_ = ch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	channel, reqs, err := ch.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		_, _ = io.Copy(channel, target)
		_ = channel.Close()
	}()
	go func() {
		_, _ = io.Copy(target, channel)
		_ = target.Close()
	}()
}
