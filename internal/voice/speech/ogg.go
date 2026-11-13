package speech

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"
)

// WaveformBins is how many waveform values a voice message carries. Element
// draws about this many bars.
const WaveformBins = 100

// OggOpusInfo describes an Ogg/Opus stream.
type OggOpusInfo struct {
	PreSkip int
	// Granule is the last page's granule position (48 kHz samples).
	Granule int64
	// PacketSizes are the audio packet sizes in bytes, headers excluded.
	PacketSizes []int
	Duration    time.Duration
}

// IsOgg reports whether data starts with an Ogg page.
func IsOgg(data []byte) bool { return len(data) >= 4 && string(data[:4]) == "OggS" }

var crcTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return
}()

func oggCRC(b []byte) uint32 {
	var c uint32
	for _, x := range b {
		c = c<<8 ^ crcTable[byte(c>>24)^x]
	}
	return c
}

// ParseOggOpus walks every page of a single-stream Ogg/Opus file, checks page
// CRCs and the Opus headers, and returns the stream's timing and packet
// sizes. Anything else is ErrNotOggOpus.
func ParseOggOpus(data []byte) (*OggOpusInfo, error) {
	if !IsOgg(data) {
		return nil, ErrNotOggOpus
	}
	info := &OggOpusInfo{}
	var packet []byte
	var packets int
	var serial uint32
	for off := 0; off < len(data); {
		if len(data)-off < 27 || string(data[off:off+4]) != "OggS" || data[off+4] != 0 {
			return nil, fmt.Errorf("%w: bad page at byte %d", ErrNotOggOpus, off)
		}
		h := data[off:]
		nseg := int(h[26])
		if len(h) < 27+nseg {
			return nil, fmt.Errorf("%w: truncated page", ErrNotOggOpus)
		}
		lacing := h[27 : 27+nseg]
		size := 27 + nseg
		for _, l := range lacing {
			size += int(l)
		}
		if len(h) < size {
			return nil, fmt.Errorf("%w: truncated page", ErrNotOggOpus)
		}
		page := append([]byte(nil), h[:size]...)
		want := binary.LittleEndian.Uint32(page[22:26])
		copy(page[22:26], []byte{0, 0, 0, 0})
		if oggCRC(page) != want {
			return nil, fmt.Errorf("%w: page checksum mismatch", ErrNotOggOpus)
		}
		s := binary.LittleEndian.Uint32(h[14:18])
		if off == 0 {
			serial = s
		} else if s != serial {
			return nil, fmt.Errorf("%w: more than one logical stream", ErrNotOggOpus)
		}
		if g := int64(binary.LittleEndian.Uint64(h[6:14])); g >= 0 {
			info.Granule = g
		}
		body := h[27+nseg : size]
		for _, l := range lacing {
			packet = append(packet, body[:l]...)
			body = body[l:]
			if l < 255 {
				switch packets {
				case 0:
					if len(packet) < 19 || !bytes.HasPrefix(packet, []byte("OpusHead")) {
						return nil, fmt.Errorf("%w: missing OpusHead", ErrNotOggOpus)
					}
					info.PreSkip = int(binary.LittleEndian.Uint16(packet[10:12]))
				case 1:
					if !bytes.HasPrefix(packet, []byte("OpusTags")) {
						return nil, fmt.Errorf("%w: missing OpusTags", ErrNotOggOpus)
					}
				default:
					info.PacketSizes = append(info.PacketSizes, len(packet))
				}
				packets++
				packet = packet[:0]
			}
		}
		off += size
	}
	if packets < 2 {
		return nil, fmt.Errorf("%w: no Opus headers", ErrNotOggOpus)
	}
	if samples := info.Granule - int64(info.PreSkip); samples > 0 {
		info.Duration = time.Duration(samples) * time.Second / 48000
	}
	return info, nil
}

// Waveform approximates loudness from packet sizes: Opus is variable
// bitrate, so quiet frames are small and speech frames are large. It returns
// bins values from 0 to 1024, scaled to the loudest bin. Decoding Opus to
// measure amplitude would need cgo; this keeps the binary pure Go and gives
// Element a waveform with the right shape.
func (i *OggOpusInfo) Waveform(bins int) []int {
	out := make([]int, bins)
	if len(i.PacketSizes) == 0 || bins <= 0 {
		return out
	}
	sums := make([]float64, bins)
	counts := make([]int, bins)
	for n, s := range i.PacketSizes {
		b := n * bins / len(i.PacketSizes)
		sums[b] += float64(s)
		counts[b]++
	}
	var max, min float64
	min = -1
	for b := range sums {
		if counts[b] == 0 {
			continue
		}
		sums[b] /= float64(counts[b])
		if sums[b] > max {
			max = sums[b]
		}
		if min < 0 || sums[b] < min {
			min = sums[b]
		}
	}
	for b := range sums {
		if counts[b] == 0 && b > 0 {
			out[b] = out[b-1]
			continue
		}
		if max > min {
			out[b] = int((sums[b] - min) / (max - min) * 1024)
		} else if max > 0 {
			out[b] = 512
		}
	}
	return out
}

// EncodeOggOpus wraps Opus packets in an Ogg stream with valid headers and
// checksums, one packet per page. Tests and fakes use it to produce voice
// notes; frameSamples is the 48 kHz length of each packet.
func EncodeOggOpus(packets [][]byte, frameSamples int) []byte {
	var out bytes.Buffer
	head := []byte("OpusHead")
	head = append(head, 1, 1)                          // version, channels
	head = binary.LittleEndian.AppendUint16(head, 312) // pre-skip
	head = binary.LittleEndian.AppendUint32(head, 48000)
	head = append(head, 0, 0, 0) // gain, mapping family
	tags := []byte("OpusTags")
	tags = binary.LittleEndian.AppendUint32(tags, 4)
	tags = append(tags, "test"...)
	tags = binary.LittleEndian.AppendUint32(tags, 0)
	var seq uint32
	page := func(p []byte, flags byte, granule int64) {
		var lacing []byte
		n := len(p)
		for n >= 255 {
			lacing = append(lacing, 255)
			n -= 255
		}
		lacing = append(lacing, byte(n))
		h := []byte("OggS")
		h = append(h, 0, flags)
		h = binary.LittleEndian.AppendUint64(h, uint64(granule))
		h = binary.LittleEndian.AppendUint32(h, 0x766f6963) // serial
		h = binary.LittleEndian.AppendUint32(h, seq)
		h = append(h, 0, 0, 0, 0, byte(len(lacing)))
		h = append(h, lacing...)
		h = append(h, p...)
		binary.LittleEndian.PutUint32(h[22:26], oggCRC(h))
		out.Write(h)
		seq++
	}
	page(head, 2, 0)
	page(tags, 0, 0)
	granule := int64(312)
	for i, p := range packets {
		granule += int64(frameSamples)
		var flags byte
		if i == len(packets)-1 {
			flags = 4
		}
		page(p, flags, granule)
	}
	return out.Bytes()
}

// SyntheticVoice returns a valid Ogg/Opus stream of about d with varying
// packet sizes. The packets are not decodable audio; it is for tests.
func SyntheticVoice(d time.Duration) []byte {
	frames := int(d / (20 * time.Millisecond))
	if frames < 1 {
		frames = 1
	}
	packets := make([][]byte, frames)
	for i := range packets {
		packets[i] = bytes.Repeat([]byte{0xfc}, 10+(i*37)%120)
	}
	return EncodeOggOpus(packets, 960)
}
