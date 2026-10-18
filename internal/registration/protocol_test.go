package registration

import (
	"bytes"
	"testing"
)

func TestProtocolRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello-payload")
	if err := EncodeFrame(&buf, Header{Op: "read", Path: "/workspace/repo/a", Offset: 4, Size: int64(len(payload)), OK: true}, payload); err != nil {
		t.Fatal(err)
	}
	header, got, err := DecodeFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if header.Op != "read" || header.Path != "/workspace/repo/a" || header.Offset != 4 || !header.OK {
		t.Fatalf("%+v", header)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("%q", got)
	}
}

func TestProtocolRejectsOversizedHeaderLen(t *testing.T) {
	var buf bytes.Buffer
	// total 4, headerLen 8 → invalid
	buf.Write([]byte{0, 0, 0, 4, 0, 0, 0, 8, 1, 2, 3, 4})
	if _, _, err := DecodeFrame(&buf); err == nil {
		t.Fatal("accepted invalid lengths")
	}
}
