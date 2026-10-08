// Package anlz reads the parts of rekordbox's ANLZ analysis files that the
// waveform harness scores: the section walk of ANLZ0000.DAT / .EXT / .2EX,
// the content path (PPTH), and the payload and header fields of the
// waveform sections PWAV, PWV2, PWV3, PWV4, PWV5, PWV6, PWV7 and PWVC.
//
// The format is owned by deadcatalog (github.com/csquared/deadcatalog/anlz,
// the full reader, and anlz/export, the writer); the layouts here are copied
// from it field for field so the two read the same bytes the same way.
// Nothing else of the format (beats, cues, phrases, VBR seek) is here.
package anlz

import (
	"encoding/binary"
	"os"
	"unicode/utf16"

	"github.com/cockroachdb/errors"
)

// File is one ANLZ file: its PMAI header and the sections after it.
type File struct {
	Header  []byte
	Tags    []Tag
	Trailer []byte
}

// Tag is one section: its FourCC, the length of its header (the offset of
// its payload within Raw) and the whole section, header included.
type Tag struct {
	FourCC    string
	LenHeader uint32
	Raw       []byte
}

// Waveform is one waveform section's entries and the fields of its header.
// EntryBytesOrKind is the first u32 of the body: the entry size for
// PWV3-7, zero for PWAV/PWV2 whose first u32 is the entry count instead.
type Waveform struct {
	Data             []byte
	EntryBytesOrKind uint32
	FourCC           string
	Format           uint32
	NumEntries       uint32
	Rate             uint16
}

// EntryBytes is the size of one entry: the header's for PWV3-7, one for
// PWAV and PWV2.
func (w Waveform) EntryBytes() uint32 {
	if w.EntryBytesOrKind != 0 {
		return w.EntryBytesOrKind
	}
	switch w.FourCC {
	case "PWAV", "PWV2":
		return 1
	default:
		return 0
	}
}

// ReadFile reads one ANLZ file.
func ReadFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Read(b)
}

// Read walks the sections of an ANLZ file: a 28-byte PMAI header (len_header
// at 4, len_file at 8), then sections of FourCC, len_header, len_tag, body.
// An unprintable FourCC ends the walk; the rest is kept as the trailer.
func Read(b []byte) (*File, error) {
	if len(b) < 28 || string(b[0:4]) != "PMAI" {
		return nil, errors.Errorf("not an ANLZ file")
	}
	lenHeader := u32(b, 4)
	lenFile := u32(b, 8)
	if int(lenHeader) > len(b) || int(lenFile) > len(b) {
		return nil, errors.Errorf("header %d / file %d exceed %d bytes", lenHeader, lenFile, len(b))
	}
	f := &File{Header: clone(b[:lenHeader])}
	for off := int(lenHeader); off+12 <= int(lenFile); {
		fourcc := b[off : off+4]
		if !printable(fourcc) {
			f.Trailer = clone(b[off:])
			return f, nil
		}
		lenHeader := u32(b, off+4)
		lenTag := u32(b, off+8)
		if lenTag < 12 || off+int(lenTag) > len(b) {
			return nil, errors.Errorf("tag %q at %d has invalid length %d", string(fourcc), off, lenTag)
		}
		f.Tags = append(f.Tags, Tag{
			FourCC:    string(fourcc),
			LenHeader: lenHeader,
			Raw:       clone(b[off : off+int(lenTag)]),
		})
		off += int(lenTag)
	}
	return f, nil
}

// Tag is the first section with that FourCC, nil without one.
func (f *File) Tag(fourcc string) *Tag {
	for i := range f.Tags {
		if f.Tags[i].FourCC == fourcc {
			return &f.Tags[i]
		}
	}
	return nil
}

// AudioPath is the content path PPTH names: a u32 byte length at 12, then
// UTF-16BE, NUL-terminated.
func (f *File) AudioPath() string {
	t := f.Tag("PPTH")
	if t == nil || len(t.Raw) < 16 {
		return ""
	}
	n := int(u32(t.Raw, 12))
	end := 16 + n
	if end > len(t.Raw) {
		end = len(t.Raw)
	}
	return utf16beString(t.Raw[16:end])
}

// Waveforms are the file's PWAV, PWV2, PWV3, PWV4, PWV5, PWV6 and PWV7
// sections in file order.
func (f *File) Waveforms() []Waveform {
	var out []Waveform
	for _, t := range f.Tags {
		if w, ok := parseWaveform(t); ok {
			out = append(out, w)
		}
	}
	return out
}

// BandScales are PWVC's three display scales (low, mid, high), the last
// six bytes of its payload, and whether the file carries them.
func (f *File) BandScales() ([3]uint16, bool) {
	t := f.Tag("PWVC")
	if t == nil {
		return [3]uint16{}, false
	}
	p := t.Payload()
	if len(p) < 6 {
		return [3]uint16{}, false
	}
	p = p[len(p)-6:]
	return [3]uint16{u16(p, 0), u16(p, 2), u16(p, 4)}, true
}

// Payload is the section after its header.
func (t Tag) Payload() []byte {
	if int(t.LenHeader) > len(t.Raw) {
		return nil
	}
	return t.Raw[t.LenHeader:]
}

// Body is the section after the twelve bytes of FourCC and lengths: where
// the waveform sections keep their own header fields.
func (t Tag) Body() []byte {
	if len(t.Raw) <= 12 {
		return nil
	}
	return t.Raw[12:]
}

// parseWaveform reads a waveform section's body. The layouts, from
// deadcatalog's reader and writer:
//
//	PWAV, PWV2  u32 entry count, u32 format (0x00010000), entries (1 byte)
//	PWV3, PWV7  u32 entry bytes, u32 entry count, u16 rate (0x0096), u16 0, entries
//	PWV4        u32 entry bytes (6), u32 entry count, u32 0, entries
//	PWV5        u32 entry bytes (2), u32 entry count, u16 rate, u8 3, u8 5, entries
//	PWV6        u32 entry bytes (3), u32 entry count, entries
func parseWaveform(t Tag) (Waveform, bool) {
	p := t.Body()
	w := Waveform{FourCC: t.FourCC}
	switch t.FourCC {
	case "PWAV", "PWV2":
		if len(p) < 8 {
			return Waveform{}, false
		}
		w.NumEntries = u32(p, 0)
		w.Format = u32(p, 4)
		w.Data = clone(p[8:])
	case "PWV3", "PWV7":
		if len(p) < 12 {
			return Waveform{}, false
		}
		w.EntryBytesOrKind = u32(p, 0)
		w.NumEntries = u32(p, 4)
		w.Rate = u16(p, 8)
		w.Data = clone(p[12:])
	case "PWV4", "PWV5":
		if len(p) < 12 {
			return Waveform{}, false
		}
		w.EntryBytesOrKind = u32(p, 0)
		w.NumEntries = u32(p, 4)
		if t.FourCC == "PWV5" {
			w.Rate = u16(p, 8)
		}
		w.Data = clone(p[12:])
	case "PWV6":
		if len(p) < 8 {
			return Waveform{}, false
		}
		w.EntryBytesOrKind = u32(p, 0)
		w.NumEntries = u32(p, 4)
		w.Data = clone(p[8:])
	default:
		return Waveform{}, false
	}
	return w, true
}

func clone(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func printable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func u16(b []byte, pos int) uint16 { return binary.BigEndian.Uint16(b[pos:]) }
func u32(b []byte, pos int) uint32 { return binary.BigEndian.Uint32(b[pos:]) }

func utf16beString(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.BigEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}
