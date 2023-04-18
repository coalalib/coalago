package newcoala

import (
	"math"
)

func newBlock(moreBlocks bool, num, size int) *block {
	block := &block{
		BlockNumber: num,
		BlockSize:   size,
		MoreBlocks:  moreBlocks,
	}
	return block
}

func newBlockFromInt(blockValue int) *block {
	block := &block{}

	block.FromInt(blockValue)

	return block
}

type block struct {
	BlockNumber int
	MoreBlocks  bool
	BlockSize   int
}

func (block *block) ToInt() int {
	if block.BlockSize > 1024 || block.BlockSize <= 0 {
		return 0
	}

	szx := computeSZX(block.BlockSize)

	m := 1
	if !block.MoreBlocks {
		m = 0
	}

	value := (block.BlockNumber << 4)
	value |= (m << 3)
	value |= (szx)

	return value
}

func (block *block) FromInt(blockValue int) error {
	num := blockValue >> 4
	m := (blockValue & 8) >> 3
	szx := blockValue & 7

	block.BlockNumber = num
	block.MoreBlocks = m != 0
	block.BlockSize = int(math.Pow(2, float64(szx+4)))

	return nil
}

/*
 * Encodes a block size into a 3-bit SZX value as specified by
 * draft-ietf-core-block-14, Section-2.2:
 *
 * 16 bytes = 2^4 --> 0
 * ...
 * 1024 bytes = 2^10 -> 6
 */
func computeSZX(blockSize int) int {
	return int(math.Log(float64(blockSize))/math.Log(2) - 4)
}
