package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFieldsReadWhatThePlayerReads(t *testing.T) {
	names, got := fields("PWV3", []byte{0xe5, 0x1f})
	assert.Equal(t, []string{"height", "white"}, names)
	assert.Equal(t, [][]byte{{5, 31}, {7, 0}}, got)

	// PWV5: red 15-13, green 12-10, blue 9-7, height 6-2.
	names, got = fields("PWV5", []byte{0b101_011_00, 0b1_10110_00})
	assert.Equal(t, []string{"red", "green", "blue", "height"}, names)
	assert.Equal(t, [][]byte{{5}, {3}, {1}, {22}}, got)

	names, got = fields("PWV7", []byte{1, 2, 3, 4, 5, 6})
	assert.Equal(t, []string{"low", "mid", "high"}, names)
	assert.Equal(t, [][]byte{{1, 4}, {2, 5}, {3, 6}}, got)

	_, got = fields("PWVC", []byte{0, 109, 0, 100, 1, 244})
	assert.Equal(t, [][]byte{{109}, {100}, {255}}, got, "a scale past a byte reads as the byte's top")
}

func TestScoreSection(t *testing.T) {
	same := scoreSection("PWV7", []byte{10, 20, 30, 40, 50, 60}, []byte{10, 20, 30, 40, 50, 60})
	require.Len(t, same.Fields, 3)
	assert.Zero(t, same.MAE)
	for _, f := range same.Fields {
		assert.Equal(t, 100.0, f.Exact)
		assert.InDelta(t, 1, f.Corr, 1e-9)
	}
	off := scoreSection("PWV7", []byte{10, 20, 30, 40, 50, 60}, []byte{12, 20, 30, 44, 50, 60})
	assert.InDelta(t, 1.0, off.MAE, 1e-9, "low lane off by 2 and 4, others exact: (3+0+0)/3")
}
