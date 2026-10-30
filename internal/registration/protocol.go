package registration

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxFrame = 32 << 20 // 32 MiB

// Header is the JSON metadata for one helper frame.
type Header struct {
	Op     string `json:"op"`
	Path   string `json:"path,omitempty"`
	To     string `json:"to,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Error  string `json:"error,omitempty"`
	OK     bool   `json:"ok,omitempty"`
}

// EncodeFrame writes one length-prefixed frame: uint32 total, uint32 headerLen, JSON header, payload.
func EncodeFrame(w io.Writer, header Header, payload []byte) error {
	hb, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if len(hb)+len(payload) > maxFrame {
		return errors.New("frame too large")
	}
	var lens [8]byte
	binary.BigEndian.PutUint32(lens[0:4], uint32(len(hb)+len(payload)))
	binary.BigEndian.PutUint32(lens[4:8], uint32(len(hb)))
	if _, err := w.Write(lens[:]); err != nil {
		return err
	}
	if _, err := w.Write(hb); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err = w.Write(payload)
	return err
}

// DecodeFrame reads one length-prefixed frame.
func DecodeFrame(r io.Reader) (Header, []byte, error) {
	header, _, payload, err := DecodeFrameRaw(r)
	return header, payload, err
}

// DecodeFrameRaw reads one frame and returns the raw JSON header bytes.
func DecodeFrameRaw(r io.Reader) (Header, []byte, []byte, error) {
	var lens [8]byte
	if _, err := io.ReadFull(r, lens[:]); err != nil {
		return Header{}, nil, nil, err
	}
	total := binary.BigEndian.Uint32(lens[0:4])
	headerLen := binary.BigEndian.Uint32(lens[4:8])
	if total > maxFrame || headerLen > total {
		return Header{}, nil, nil, fmt.Errorf("invalid frame lengths %d/%d", headerLen, total)
	}
	body := make([]byte, total)
	if _, err := io.ReadFull(r, body); err != nil {
		return Header{}, nil, nil, err
	}
	rawHeader := body[:headerLen]
	var header Header
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		return Header{}, nil, nil, err
	}
	return header, append([]byte{}, rawHeader...), body[headerLen:], nil
}
