package qrcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// Ecc is the representation of an error correction level in a QR Code symbol.
type Ecc int

const (
	Low      Ecc = iota // 7% of codewords can be restored
	Medium              // 15% of codewords can be restored
	Quartile            // 25% of codewords can be restored
	High                // 30% of codewords can be restored
)

// eccFormats maps the ECC to its respective format bits.
var eccFormats = [...]int{1, 0, 3, 2}

// FormatBits method gets the format bits associated with the error correction level.
func (e Ecc) FormatBits() int {
	return eccFormats[e]
}

// Minimum(1) and Maximum(40) version numbers based on the QR Code Model 2 standard
const (
	MinVersion = 1
	MaxVersion = 40
)

// penaltyN1 - N4 are constants used in QR Code masking penalty rules.
const (
	penaltyN1 = 3
	penaltyN2 = 3
	penaltyN3 = 40
	penaltyN4 = 10
)

// getEccCodeWordsPerBlock function provides a lookup table for the number of error correction code words per block
// for different versions and error correction levels of the QR Code.
func getEccCodeWordsPerBlock() [][]int8 {
	return [][]int8{
		// Version: (note that index 0 is for padding, and is set to an illegal value)
		// 0,  1,  2,  3,  4,  5,  6,  7,  8,  9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40    Error correction level
		{-1, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},  // Low
		{-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28}, // Medium
		{-1, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24, 28, 26, 24, 20, 30, 24, 28, 28, 26, 30, 28, 30, 30, 30, 30, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30}, // Quartile
		{-1, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28, 24, 28, 22, 24, 24, 30, 28, 28, 26, 28, 30, 24, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30}, // High
	}
}

// getNumErrorCorrectionBlocks function provides a lookup table for the number of error correction blocks required
// for different versions and error correction levels of the QR Code.
func getNumErrorCorrectionBlocks() [][]int8 {
	return [][]int8{
		// Version: (note that index 0 is for padding, and is set to an illegal value)
		// 0, 1, 2, 3, 4, 5, 6, 7, 8, 9,10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40    Error correction level
		{-1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25},              // Low
		{-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49},     // Medium
		{-1, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29, 34, 34, 35, 38, 40, 43, 45, 48, 51, 53, 56, 59, 62, 65, 68},  // Quartile
		{-1, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35, 37, 40, 42, 45, 48, 51, 54, 57, 60, 63, 66, 70, 74, 77, 81}, // High
	}
}

// QrCode is the representation of a QR code
type QrCode struct {
	version              int // Version of the QR Code.
	size                 int // Size of the QR Code.
	errorCorrectionLevel Ecc // Error correction level (ECC) of the QR Code.
	mask                 int // Mask pattern of the QR Code.

	modules    [][]bool // 2D boolean matrix representing dark modules in the QR Code.
	isFunction [][]bool // 2D boolean matrix distinguishing function from data modules.
}

// newQrCode is used to create a new QR code with the provided version(ver), error correction level(ecl),
// data codewords (dataCodewords) and mask value (msk).
func newQrCode(ver int, ecl Ecc, dataCodewords []byte, msk int) (*QrCode, error) {
	if msk < -1 || msk > 7 {
		return nil, errors.New("mask value out of range")
	}

	qrCode := &QrCode{
		version:              ver,
		size:                 ver*4 + 17, // Calculate size based on version
		errorCorrectionLevel: ecl,
	}

	modules := make([][]bool, qrCode.size)
	isFunction := make([][]bool, qrCode.size)
	for i := 0; i < qrCode.size; i++ {
		modules[i] = make([]bool, qrCode.size)
		isFunction[i] = make([]bool, qrCode.size)
	}
	qrCode.modules = modules
	qrCode.isFunction = isFunction

	// Draw function patterns on the QR Code
	qrCode.drawFunctionPatterns()

	// Add error correction and interleave the data codewords
	allCodewords, err := qrCode.addEccAndInterLeave(dataCodewords)
	if err != nil {
		return nil, err
	}

	err = qrCode.drawCodewords(allCodewords)
	if err != nil {
		return nil, err
	}

	// If mask is -1, choose the best mask based on minimizing penalty score
	if msk == -1 {
		minPenalty := math.MaxInt32
		for i := 0; i < 8; i++ {
			err = qrCode.applyMask(i)
			if err != nil {
				return nil, err
			}
			qrCode.drawFormatBits(i)
			penalty := qrCode.getPenaltyScore()
			if penalty < minPenalty {
				msk = i
				minPenalty = penalty
			}
			err = qrCode.applyMask(i)
			if err != nil {
				return nil, err
			}
		}
	}

	// Apply the selected mask
	qrCode.mask = msk
	err = qrCode.applyMask(msk)
	if err != nil {
		return nil, err
	}

	// Draw format bits for the mask
	qrCode.drawFormatBits(msk)
	qrCode.isFunction = nil

	return qrCode, nil
}

// GetSize returns the size of the QR code
func (q *QrCode) GetSize() int {
	return q.size
}

// GetModule checks if a module is dark at given coordinates.
func (q *QrCode) GetModule(x, y int) bool {
	return 0 <= x && x < q.size && 0 <= y && y < q.size && q.modules[y][x]
}

// setFunctionModule sets a given module's status and function status in QrCode.
// The method takes coordinates x, y and isDark - a flag indicating whether the module should be dark or not.
func (q *QrCode) setFunctionModule(x, y int, isDark bool) {
	// Assigning darkness state to the respective module in the QR Code.
	q.modules[y][x] = isDark
	// Marking this module as a function module.
	q.isFunction[y][x] = true
}

// addEccAndInterLeave adds Error Correction Code (ECC) and interleaves to the data received.
// This method takes an array of bytes representing the data that needs to be encoded into a QR code.
// It returns the data with added ECC and after interleaving, or an error if something goes wrong.
func (q *QrCode) addEccAndInterLeave(data []byte) ([]byte, error) {
	// Getting the number of data codewords for the current version and error correction level
	numDataCodewords := getNumDataCodewords(q.version, q.errorCorrectionLevel)

	// Checking if the input data length is equal to the number of data codewords
	if len(data) != numDataCodewords {
		return nil, errors.New("invalid argument")
	}

	// Get the number of blocks and block ECC length based on the errorCorrectionLevel and QR code version
	numBlocks := getNumErrorCorrectionBlocks()[q.errorCorrectionLevel][q.version]
	blockEccLen := getEccCodeWordsPerBlock()[q.errorCorrectionLevel][q.version]
	rawCodewords := getNumRawDataModules(q.version) / 8

	// Calculate the number of short blocks
	numShortBlocks := int(numBlocks) - rawCodewords%int(numBlocks)
	// Calculate the length of short blocks
	shortBlockLen := rawCodewords / int(numBlocks)

	blocks := make([][]byte, numBlocks)
	// Compute reed solomon divisor
	rsDiv, err := reedSolomonComputeDivisor(int(blockEccLen))
	if err != nil {
		return nil, err
	}
	for i, k := 0, 0; i < int(numBlocks); i++ {
		index := 1
		if i < numShortBlocks {
			index = 0
		}

		// Prepare the data to be encoded
		dat := make([]byte, shortBlockLen-int(blockEccLen)+index)
		copy(dat, data[k:k+shortBlockLen-int(blockEccLen)+index])
		k += len(dat)

		// Prepare the block to store the encoded data and the ECC
		block := make([]byte, shortBlockLen+1)
		copy(block, dat)

		// Calculate the ECC for the data
		ecc := reedSolomonComputeRemainder(dat, rsDiv)

		// Append the ECC to the end of the data
		copy(block[len(block)-int(blockEccLen):], ecc)
		blocks[i] = block
	}

	res := make([]byte, rawCodewords)
	for i, k := 0, 0; i < len(blocks[0]); i++ {
		for j := 0; j < len(blocks); j++ {
			if i != shortBlockLen-int(blockEccLen) || j >= numShortBlocks {
				res[k] = blocks[j][i]
				k++
			}
		}
	}
	return res, nil
}

// drawCodewords fills up the QR code's modules based on the input data.
func (q *QrCode) drawCodewords(data []byte) error {
	// getNumRawDataModules estimates the number of data bits that can be stored for a given version of QR Code
	// The result is divided by 8 to find the number of bytes available.
	numRawDataModules := getNumRawDataModules(q.version) / 8
	if len(data) != numRawDataModules {
		return errors.New("illegal argument")
	}

	i := 0
	// Iterate over QR Code grid from right-to-left.
	for right := q.size - 1; right >= 1; right -= 2 {
		// Skip column at index 6 as it's reserved for timing patterns in QR Code.
		if right == 6 {
			right = 5
		}
		// Iterate over each row.
		for vert := 0; vert < q.size; vert++ {
			// Check two adjacent pixels.
			for j := 0; j < 2; j++ {
				x := right - j
				// This checks if we're going upwards in the current two-column section of the QR Code.
				upward := ((right + 1) & 2) == 0
				y := vert
				if upward {
					// If we're going upwards, calculate the corresponding y-coordinate.
					y = q.size - 1 - vert
				}
				// Check if current module is not a function pattern and there's data left to encode.
				if !q.isFunction[y][x] && i < len(data)*8 {
					// Write bits into QR Code. Use bitwise operations to extract individual bits from each byte of data.
					q.modules[y][x] = getBit(int(data[i>>3]), 7-(i&7))
					i++
				}
			}
		}
	}
	return nil
}

// applyMask applies the chosen mask pattern to the QR code.
func (q *QrCode) applyMask(msk int) error {
	if msk < 0 || msk > 7 {
		return errors.New("mask value out of range")
	}

	for y := 0; y < q.size; y++ {
		for x := 0; x < q.size; x++ {
			// A boolean variable which will decide if the current cell needs to be inverted or not.
			var invert bool
			// Each case corresponds to a different mask pattern defined by the QR code specification.
			switch msk {
			case 0:
				invert = (x+y)%2 == 0
			case 1:
				invert = y%2 == 0
			case 2:
				invert = x%3 == 0
			case 3:
				invert = (x+y)%3 == 0
			case 4:
				invert = (x/3+y/2)%2 == 0
			case 5:
				invert = x*y%2+x*y%3 == 0
			case 6:
				invert = (x*y%2+x*y%3)%2 == 0
			case 7:
				invert = ((x+y)%2+x*y%3)%2 == 0
			}
			// Invert the cell's color if it is not a function pattern cell and the 'invert' variable is true.
			q.modules[y][x] = q.modules[y][x] != (invert && !q.isFunction[y][x])
		}
	}
	return nil
}

// getPenaltyScore is a method of the QrCode struct that
// calculates and returns a penalty score based on several criteria.
func (q *QrCode) getPenaltyScore() int {
	res := 0
	// Calculate penalties in the horizontal direction
	for y := 0; y < q.size; y++ {
		runColor, runX := false, 0
		runHistory := make([]int, 7)
		for x := 0; x < q.size; x++ {
			if q.modules[y][x] == runColor {
				runX++
				if runX == 5 {
					res += penaltyN1
				} else if runX > 5 {
					res++
				}
			} else {
				q.finderPenaltyAddHistory(runX, runHistory)
				// If the color run was for white pixels, calculate additional penalties
				if !runColor {
					res += q.finderPenaltyCountPatterns(runHistory) * penaltyN3
				}
				runColor = q.modules[y][x]
				runX = 1
			}
		}
		// After evaluating all pixels in the row, check for finder pattern violation
		res += q.finderPenaltyTerminateAndCount(runColor, runX, runHistory) * penaltyN3
	}

	// Repeat similar process for vertical direction
	for x := 0; x < q.size; x++ {
		runColor, runY := false, 0
		runHistory := make([]int, 7)
		for y := 0; y < q.size; y++ {
			if q.modules[y][x] == runColor {
				runY++
				if runY == 5 {
					res += penaltyN1
				} else if runY > 5 {
					res++
				}
			} else {
				q.finderPenaltyAddHistory(runY, runHistory)
				if !runColor {
					res += q.finderPenaltyCountPatterns(runHistory) * penaltyN3
				}
				runColor = q.modules[y][x]
				runY = 1
			}
		}
		res += q.finderPenaltyTerminateAndCount(runColor, runY, runHistory) * penaltyN3
	}

	for y := 0; y < q.size-1; y++ {
		for x := 0; x < q.size-1; x++ {
			color := q.modules[y][x]
			// If 2x2 block has the same color, increase penalty
			if color == q.modules[y][x+1] &&
				color == q.modules[y+1][x] &&
				color == q.modules[y+1][x+1] {
				res += penaltyN2
			}
		}
	}

	// Count the total number of dark modules
	dark := 0
	for _, row := range q.modules {
		for _, color := range row {
			if color {
				dark++
			}
		}
	}

	// Compute the ratio of dark modules to total modules, compare to ideal ratio and apply penalty
	total := q.size * q.size
	k := (abs(dark*20-total*10)+total-1)/total - 1
	res += k * penaltyN4
	return res
}

// drawAlignmentPattern draws an alignment pattern centered at the given coordinates (x, y).
// Alignment patterns are part of the QR code that enable readers to accurately read the data,
// even if the image is distorted (e.g., skewed or twisted).
func (q *QrCode) drawAlignmentPattern(x, y int) {
	// We scan a 5x5 region around the center module (specified by x, y).
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			// For each scanned module, we use setFunctionModule to either color it or not,
			// depending on its distance from the central module.
			// Modules farther away from the center are made dark (isDark = true), except for those directly adjacent to the center.
			q.setFunctionModule(x+dx, y+dy, max(abs(dx), abs(dy)) != 1)
		}
	}
}

// drawFinderPattern draws a finder pattern centered at the given coordinates (x, y).
// Finder patterns are located at three corners in QR Codes and allow the QR Code to be read from any direction.
func (q *QrCode) drawFinderPattern(x, y int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			dist := max(abs(dx), abs(dy))
			xx, yy := x+dx, y+dy
			if 0 <= xx && xx < q.size && 0 <= yy && yy < q.size {
				q.setFunctionModule(xx, yy, dist != 2 && dist != 4)
			}
		}
	}
}

// drawVersion encodes version information into the QR Code. Version information is only included
// for QR Codes with a version number of 7 or higher.
func (q *QrCode) drawVersion() {
	if q.version < 7 {
		return
	}

	rem := q.version
	for i := 0; i < 12; i++ {
		rem = (rem << 1) ^ ((rem >> 11) * 0x1F25) // Perform calculation to derive final remainder
	}
	bits := q.version<<12 | rem

	// Draw two copies
	for i := 0; i < 18; i++ {
		bit := getBit(bits, i)
		a := q.size - 11 + i%3
		b := i / 3
		q.setFunctionModule(a, b, bit)
		q.setFunctionModule(b, a, bit)
	}
}

// drawFunctionPatterns adds all the function patterns (including format/version info, timing patterns,
// alignment patterns, and finder patterns) to the QR Code matrix.
func (q *QrCode) drawFunctionPatterns() {
	// Draw horizontal and vertical timing patterns
	for i := 0; i < q.size; i++ {
		q.setFunctionModule(6, i, i%2 == 0)
		q.setFunctionModule(i, 6, i%2 == 0)
	}

	// Draw 3 finder patterns
	q.drawFinderPattern(3, 3)
	q.drawFinderPattern(q.size-4, 3)
	q.drawFinderPattern(3, q.size-4)

	// Get the positions of the alignment patterns, then draw them
	alignPatPos := q.getAlignmentPatternPositions()
	numAlign := len(alignPatPos)
	for i := 0; i < numAlign; i++ {
		for j := 0; j < numAlign; j++ {
			if !(i == 0 && j == 0 || i == 0 && j == numAlign-1 || i == numAlign-1 && j == 0) {
				q.drawAlignmentPattern(alignPatPos[i], alignPatPos[j])
			}
		}
	}

	q.drawFormatBits(0)
	q.drawVersion()
}

// getAlignmentPatternPositions returns the positions of the alignment patterns in the QR code based on its version.
// If the version is 1, it returns an empty int slice because there's no alignment pattern in this case.
func (q *QrCode) getAlignmentPatternPositions() []int {
	if q.version == 1 {
		return []int{}
	} else {
		numAlign := q.version/7 + 2 // Calculation of number of alignment patterns based on version of QR Code.
		step := 0
		if q.version == 32 {
			step = 26
		} else {
			step = (q.version*4 + numAlign*2 + 1) / (numAlign*2 - 2) * 2 // Calculation of step size for placing the alignment patterns.
		}

		res := make([]int, numAlign)
		res[0] = 6
		for i, pos := len(res)-1, q.size-7; i >= 1; {
			res[i] = pos
			i--
			pos -= step
		}

		return res
	}
}

// drawFormatBits encodes format information (error correction level and mask number) into the QR Code's format bits.
func (q *QrCode) drawFormatBits(msk int) {
	data := q.errorCorrectionLevel.FormatBits()<<3 | msk
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537) // Computes the remainder of the polynomial division
	}

	bits := (data<<10 | rem) ^ 0x5412 // Combines the data, remainder and additional bit string

	for i := 0; i <= 5; i++ {
		q.setFunctionModule(8, i, getBit(bits, i))
	}
	q.setFunctionModule(8, 7, getBit(bits, 6))
	q.setFunctionModule(8, 8, getBit(bits, 7))
	q.setFunctionModule(7, 8, getBit(bits, 8))

	for i := 9; i < 15; i++ {
		q.setFunctionModule(14-i, 8, getBit(bits, i))
	}

	for i := 0; i < 8; i++ {
		q.setFunctionModule(q.size-1-i, 8, getBit(bits, i))
	}

	for i := 8; i < 15; i++ {
		q.setFunctionModule(8, q.size-15+i, getBit(bits, i))
	}
	q.setFunctionModule(8, q.size-8, true)
}

func Encode(data any, e ...Ecc) (*QrCode, error) {
	text, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return EncodeBinary(text, e...)
}

// EncodeText takes a string and an error correction level (ecl),
// encodes the text to segments and returns a QR code or an error.
func EncodeText(text string, e ...Ecc) (*QrCode, error) {
	var ecl Ecc
	if len(e) > 0 {
		ecl = e[0]
	} else {
		ecl = Low
	}
	segs, err := MakeSegments(text)
	if err != nil {
		return nil, err
	}

	return EncodeStandardSegments(segs, ecl)
}

// EncodeBinary takes a byte array and an error correction level (ecl),
// converts the bytes to QR code segments and returns a QR code or an error.
func EncodeBinary(data []byte, e ...Ecc) (*QrCode, error) {
	var ecl Ecc
	if len(e) > 0 {
		ecl = e[0]
	} else {
		ecl = Low
	}
	segs, err := MakeBytes(data)
	if err != nil {
		return nil, err
	}

	return EncodeStandardSegments([]*QrSegment{segs}, ecl)
}

// EncodeStandardSegments takes QR code segments and an error correction level,
// creates a standard QR code using these parameters and returns it or an error.
func EncodeStandardSegments(segs []*QrSegment, ecl Ecc) (*QrCode, error) {
	return EncodeSegments(segs, ecl, MinVersion, MaxVersion, -1, true)
}

// EncodeSegments is a more flexible version of EncodeStandardSegments. It allows
// the specification of minVer, maxVer, mask in addition to the regular parameters.
// Returns a QR code object or an error.
func EncodeSegments(segs []*QrSegment, ecl Ecc, minVer, maxVer, mask int, boostEcl bool) (*QrCode, error) {
	if segs == nil {
		return nil, errors.New("slice of QrSegment is nil")
	}

	if !isValidVersion(minVer, maxVer) {
		return nil, errors.New("invalid version")
	}

	// Loop over all versions between minVer and maxVer to find a suitable one
	version, dataUsedBits := 0, 0
	for version = minVer; ; version++ {
		// Calculate data capacity bits
		dataCapacityBits := getNumDataCodewords(version, ecl) * 8
		// Count total bits used
		dataUsedBits = getTotalBits(segs, version)
		if dataUsedBits != -1 && dataUsedBits <= dataCapacityBits {
			break
		}

		// If no suitable version found then throw a Segment too long error
		if version >= maxVer {
			msg := "Segment too long"
			if dataUsedBits != -1 {
				msg = fmt.Sprintf("Data length = %d bits, Max capacity = %d bits", dataUsedBits, dataCapacityBits)
			}
			return nil, &DataTooLongException{Msg: msg}
		}
	}

	// If boostEcl is set to true, try to upgrade the error correction level
	// as far as the data can fit.
	for _, newEcl := range []Ecc{Medium, Quartile, High} {
		numDataCodewords := getNumDataCodewords(version, newEcl)
		if boostEcl && dataUsedBits <= numDataCodewords*8 {
			ecl = newEcl
		}
	}

	bb := BitBuffer{}
	for _, seg := range segs {
		if seg == nil {
			continue
		}

		err := bb.appendBits(seg.mode.modeBits, 4)
		if err != nil {
			return nil, err
		}
		err = bb.appendBits(seg.numChars, seg.mode.numCharCountBits(version))
		if err != nil {
			return nil, err
		}
		err = bb.appendData(seg.data)
		if err != nil {
			return nil, err
		}
	}

	// Getting the final data capacity after all segments have been processed.
	dataCapacityBits := getNumDataCodewords(version, ecl) * 8
	err := bb.appendBits(0, min(4, dataCapacityBits-bb.len()))
	if err != nil {
		return nil, err
	}

	err = bb.appendBits(0, (8-bb.len()%8)%8)
	if err != nil {
		return nil, err
	}

	// Writing pad bytes until the BitBuffer length reaches the final data capacity
	for padByte := 0xEC; bb.len() < dataCapacityBits; padByte ^= 0xEC ^ 0x11 {
		err = bb.appendBits(padByte, 8)
		if err != nil {
			return nil, err
		}
	}

	dataCodewords := make([]byte, bb.len()/8)
	for i := 0; i < bb.len(); i++ {
		bit := 0
		if bb.getBit(i) {
			bit = 1
		}
		dataCodewords[i>>3] |= byte(bit << (7 - (i & 7)))
	}
	return newQrCode(version, ecl, dataCodewords, mask)
}

// isValidVersion is a function that checks if the given minVer and maxVer are within the valid QR code version range.
// The function returns true if both minVer and maxVer lie between the constant values MinVersion and MaxVersion (inclusive).
func isValidVersion(minVer, maxVer int) bool {
	return MinVersion <= minVer && minVer <= maxVer && maxVer <= MaxVersion
}

// getNumDataCodewords function calculates the number of data codewords for a given version and error correction level.
func getNumDataCodewords(ver int, ecl Ecc) int {
	eccCodewordsPerBlock := getEccCodeWordsPerBlock()
	numRawDataModules := getNumRawDataModules(ver)
	numErrorCorrectionBlocks := getNumErrorCorrectionBlocks()
	return numRawDataModules/8 -
		int(eccCodewordsPerBlock[ecl][ver])*int(numErrorCorrectionBlocks[ecl][ver])
}

// finderPenaltyCountPatterns is a method of QrCode structure.
// It checks if patterns in runHistory follow a specific pattern (1:1:3:1:1 ratio) and returns a penalty score.
// The parameter runHistory is an array of recent pixel colors (0 or 1), where each number is a count of consecutive pixels of that color.
func (q *QrCode) finderPenaltyCountPatterns(runHistory []int) int {
	n := runHistory[1]

	// core checks whether the middle part of the pattern matches the ratio 1:3:1
	core := n > 0 && runHistory[2] == n && runHistory[3] == n*3 && runHistory[4] == n && runHistory[5] == n

	res := 0
	// Check if both sides of the core pattern have at least 4 modules of white pixels.
	if core && runHistory[0] >= n*4 && runHistory[6] >= n {
		res = 1
	}

	// Check if both sides of the core pattern have at least 4 modules of black pixels.
	if core && runHistory[6] >= n*4 && runHistory[0] >= n {
		res += 1
	}
	return res
}

// finderPenaltyTerminateAndCount is a method of QrCode structure.
// It finalizes the history of seen modules when the color changes and calculates the penalty score.
// currentRunColor is a boolean representing the current color (false=white, true=black).
// currentRunLen is the count of consecutive modules of the same color.
// runHistory is an array of counts of alternating color runs, ending with the most recent color.
func (q *QrCode) finderPenaltyTerminateAndCount(currentRunColor bool, currentRunLen int, runHistory []int) int {
	if currentRunColor {
		q.finderPenaltyAddHistory(currentRunLen, runHistory)
		currentRunLen = 0
	}

	currentRunLen += q.size
	q.finderPenaltyAddHistory(currentRunLen, runHistory)
	return q.finderPenaltyCountPatterns(runHistory)
}

// getNumRawDataModules calculates the number of raw data modules for a specific QR code version.
func getNumRawDataModules(ver int) int {
	// Calculate the size of the QR code grid.
	// For each version, the size increases by 4 modules.
	size := ver*4 + 17

	// Start with the total number of modules in the QR code grid (size^2)
	res := size * size

	// Subtract the three position detection patterns (each is 8x8 modules)
	res -= 8 * 8 * 3

	// Subtract the two horizontal timing patterns and the two vertical timing patterns
	// (each is 15 modules long), along with the single dark module reserved for format information
	res -= 15*2 + 1

	// Subtract the border modules around the timing patterns
	res -= (size - 16) * 2

	// If version is 2 or higher, there are alignment patterns
	if ver >= 2 {
		// Get the number of alignment patterns for this version of QR code
		numAlign := ver/7 + 2

		// Subtract the space taken up by the alignment patterns (each is 5x5 modules)
		res -= (numAlign - 1) * (numAlign - 1) * 25

		// Subtract the two sets of border modules around the alignment patterns
		res -= (numAlign - 2) * 2 * 20

		// For versions 7 and above, subtract the space for version information (6x3 modules on both sides)
		if ver >= 7 {
			res -= 6 * 3 * 2
		}
	}
	return res
}

// reedSolomonComputeDivisor computes a Reed-Solomon divisor for a given degree.
// The degree must be between 1 and 255 inclusive and determines the size of the output byte slice.
// The Reed-Solomon divisor computed by this function is used in error detection and correction codes.
func reedSolomonComputeDivisor(degree int) ([]byte, error) {
	if degree < 1 || degree > 255 {
		return nil, errors.New("degree out of range")
	}

	res := make([]byte, degree)
	res[degree-1] = 1

	root := 1
	for i := 0; i < degree; i++ {
		for j := 0; j < len(res); j++ {
			// Multiply the jth element of res by root using Reed-Solomon multiplication
			res[j] = byte(reedSolomonMultiply(int(res[j]&0xFF), root))
			if j+1 < len(res) {
				res[j] ^= res[j+1]
			}
		}
		root = reedSolomonMultiply(root, 0x02)
	}
	return res, nil
}

// reedSolomonComputeRemainder computes the remainder of Reed-Solomon encoding.
// Reed-Solomon is an error correction technique used in QR codes (and other data storage).
// This function takes two parameters: data and divisor which are both slices of bytes.
func reedSolomonComputeRemainder(data, divisor []byte) []byte {
	res := make([]byte, len(divisor))
	for _, b := range data {
		factor := (b ^ res[0]) & 0xFF
		copy(res, res[1:])
		res[len(res)-1] = byte(0)
		for i := 0; i < len(res); i++ {
			res[i] ^= byte(reedSolomonMultiply(int(divisor[i]&0xFF), int(factor)))
		}
	}
	return res
}

// reedSolomonMultiply is a helper function that performs multiplication in Galois Field 2^8.
// It takes two integer parameters x and y.
func reedSolomonMultiply(x, y int) int {
	z := 0
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		z ^= ((y >> i) & 1) * x
	}
	return z
}

// finderPenaltyAddHistory is a method on the QrCode struct which updates
// the history of run lengths with the current run length.
//
// This method is likely part of the QR code error correction process,
// which uses historical data to identify and correct errors.
//
// The penalty score increases when there are many blocks of modules that have the same color in a row,
// or there are patterns that look similar to the position detection pattern.
// Here we're calculating the penalty based on the length of consecutive runs of same-color modules.
func (q *QrCode) finderPenaltyAddHistory(currentRunLen int, runHistory []int) {
	if runHistory[0] == 0 {
		currentRunLen += q.size
	}
	copy(runHistory[1:], runHistory[:len(runHistory)-1])
	runHistory[0] = currentRunLen
}

// getBit gets the bit at position i from x.
func getBit(x, i int) bool {
	return ((x >> uint(i)) & 1) != 0
}
