package main

import (
	"encoding/binary"
	"math"
)

// Sections are the waveform sections scored, in file order.
var Sections = []string{"PWAV", "PWV2", "PWV3", "PWV4", "PWV5", "PWV6", "PWV7", "PWVC"}

// Field is one value a player reads from a section (a band, a colour, a
// height), compared column by column.
type Field struct {
	Name     string
	Exact    float64 // percent of columns equal
	MAE      float64 // mean absolute error, in the field's own units
	Corr     float64 // Pearson correlation
	RefLen   int
	GotLen   int
	Compared int
}

// SectionScore is a section's fields and their mean MAE, the number a floor
// holds.
type SectionScore struct {
	Section string
	Fields  []Field
	MAE     float64
}

// scoreSection compares a generated section with rekordbox's, field by field.
func scoreSection(section string, ref, got []byte) SectionScore {
	names, refFields := fields(section, ref)
	_, gotFields := fields(section, got)
	out := SectionScore{Section: section}
	for i, name := range names {
		f := compare(refFields[i], gotFields[i])
		f.Name = name
		out.Fields = append(out.Fields, f)
		out.MAE += f.MAE
	}
	if len(names) > 0 {
		out.MAE /= float64(len(names))
	}
	return out
}

// fields splits a section's entries into what the player reads: PWAV/PWV3
// height (5 bits) and whiteness (3); PWV2 height; PWV4's six lanes; PWV5's
// red, green, blue (3 bits each) and height (5); PWV6/PWV7 low, mid, high;
// PWVC's three display scales.
func fields(section string, b []byte) ([]string, [][]byte) {
	every := func(stride int, f func(i int) byte) []byte {
		var out []byte
		for i := 0; i+stride <= len(b); i += stride {
			out = append(out, f(i))
		}
		return out
	}
	lanes := func(names ...string) ([]string, [][]byte) {
		out := make([][]byte, len(names))
		for lane := range names {
			out[lane] = every(len(names), func(i int) byte { return b[i+lane] })
		}
		return names, out
	}
	switch section {
	case "PWAV", "PWV3":
		return []string{"height", "white"}, [][]byte{every(1, func(i int) byte { return b[i] & 0x1f }), every(1, func(i int) byte { return b[i] >> 5 })}
	case "PWV2":
		return []string{"height"}, [][]byte{every(1, func(i int) byte { return b[i] & 0x0f })}
	case "PWV4":
		return lanes("l0", "l1", "l2", "l3", "l4", "l5")
	case "PWV5":
		u := func(i int) uint16 { return binary.BigEndian.Uint16(b[i:]) }
		return []string{"red", "green", "blue", "height"}, [][]byte{
			every(2, func(i int) byte { return byte(u(i) >> 13 & 7) }),
			every(2, func(i int) byte { return byte(u(i) >> 10 & 7) }),
			every(2, func(i int) byte { return byte(u(i) >> 7 & 7) }),
			every(2, func(i int) byte { return byte(u(i) >> 2 & 0x1f) }),
		}
	case "PWV6", "PWV7":
		return lanes("low", "mid", "high")
	case "PWVC":
		scale := func(i int) []byte {
			if len(b) < i+2 {
				return nil
			}
			return []byte{byte(min(255, int(binary.BigEndian.Uint16(b[i:]))))}
		}
		return []string{"low", "mid", "high"}, [][]byte{scale(0), scale(2), scale(4)}
	}
	return []string{"bytes"}, [][]byte{b}
}

func compare(ref, got []byte) Field {
	n := min(len(ref), len(got))
	f := Field{RefLen: len(ref), GotLen: len(got), Compared: n}
	if n == 0 {
		return f
	}
	var sumRef, sumGot, exact, abs float64
	for i := 0; i < n; i++ {
		sumRef += float64(ref[i])
		sumGot += float64(got[i])
		if ref[i] == got[i] {
			exact++
		}
		abs += math.Abs(float64(ref[i]) - float64(got[i]))
	}
	meanRef, meanGot := sumRef/float64(n), sumGot/float64(n)
	var cov, varRef, varGot float64
	for i := 0; i < n; i++ {
		dr, dg := float64(ref[i])-meanRef, float64(got[i])-meanGot
		cov += dr * dg
		varRef += dr * dr
		varGot += dg * dg
	}
	f.Exact = 100 * exact / float64(n)
	f.MAE = abs / float64(n)
	if varRef > 0 && varGot > 0 {
		f.Corr = cov / math.Sqrt(varRef*varGot)
	}
	return f
}
