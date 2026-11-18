package routeros_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mmdelhajj/paxve/internal/routeros"
	"github.com/mmdelhajj/paxve/internal/routeros/fakerouter"
)

func TestLengthEncoding(t *testing.T) {
	cases := map[int][]byte{
		0:          {0x00},
		0x7F:       {0x7F},
		0x80:       {0x80, 0x80},
		0x3FFF:     {0xBF, 0xFF},
		0x4000:     {0xC0, 0x40, 0x00},
		0x1FFFFF:   {0xDF, 0xFF, 0xFF},
		0x200000:   {0xE0, 0x20, 0x00, 0x00},
		0xFFFFFFF:  {0xEF, 0xFF, 0xFF, 0xFF},
		0x10000000: {0xF0, 0x10, 0x00, 0x00, 0x00},
	}
	for n, want := range cases {
		got := routeros.EncodeLength(n)
		if !bytes.Equal(got, want) {
			t.Fatalf("EncodeLength(%#x) = % x, want % x", n, got, want)
		}
		back, err := routeros.ReadLength(bytes.NewReader(got))
		if err != nil || back != n {
			t.Fatalf("ReadLength(% x) = %#x, %v; want %#x", got, back, err, n)
		}
	}
}

func TestSentenceRoundTrip(t *testing.T) {
	long := string(bytes.Repeat([]byte("x"), 300))
	raw := routeros.EncodeSentence("/ppp/secret/print", "?name=ali", "=comment="+long)
	words, err := routeros.ReadSentence(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil || len(words) != 3 || words[2] != "=comment="+long {
		t.Fatalf("round trip failed: %v %v", words, err)
	}
	a := routeros.Attrs([]string{"=.id=*1", "=name=ali", "=comment=a=b", "?ignored=1"})
	if a[".id"] != "*1" || a["name"] != "ali" || a["comment"] != "a=b" || len(a) != 3 {
		t.Fatalf("Attrs = %v", a)
	}
}

func TestClientAgainstFakeRouter(t *testing.T) {
	fr, err := fakerouter.Start("api", "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	fr.AddSecret("ali")
	ctx := context.Background()

	bad, err := routeros.Dial(ctx, "127.0.0.1", fr.Port(), false, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var re *routeros.Error
	if err := bad.Login("api", "wrong"); !errors.As(err, &re) {
		t.Fatalf("wrong password: %v", err)
	}
	bad.Close()

	c, err := routeros.Dial(ctx, "127.0.0.1", fr.Port(), false, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("api", "pw"); err != nil {
		t.Fatal(err)
	}
	id, ver, err := c.Identity()
	if err != nil || id != "FakeRouter" || ver == "" {
		t.Fatalf("identity %q %q %v", id, ver, err)
	}
	if err := c.SetUserDisabled("pppoe", "ali", true); err != nil {
		t.Fatal(err)
	}
	if d, _ := fr.Disabled("ali"); !d || fr.ActiveSessions("ali") != 0 {
		t.Fatalf("not disabled/kicked: disabled=%v sessions=%d", d, fr.ActiveSessions("ali"))
	}
	if err := c.SetUserDisabled("pppoe", "ali", false); err != nil {
		t.Fatal(err)
	}
	if d, _ := fr.Disabled("ali"); d {
		t.Fatal("still disabled")
	}
	if err := c.SetUserDisabled("pppoe", "nobody", true); !errors.Is(err, routeros.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
}

func TestDialTimeout(t *testing.T) {
	start := time.Now()
	_, err := routeros.Dial(context.Background(), "10.255.255.1", 8728, false, 500*time.Millisecond)
	if err == nil {
		t.Skip("unexpectedly reachable")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("dial took %v", time.Since(start))
	}
}
