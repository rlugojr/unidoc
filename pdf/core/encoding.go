/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.md', which is part of this source code package.
 */

package core

// Implement encoders for PDF. Currently supported:
// - Raw (Identity)
// - FlateDecode
// - LZW
// - ASCII Hex

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"io"

	// Need two slightly different implementations of LZW (EarlyChange parameter).
	lzw0 "compress/lzw"
	lzw1 "golang.org/x/image/tiff/lzw"

	"github.com/unidoc/unidoc/common"
)

type StreamEncoder interface {
	EncodeBytes(data []byte) ([]byte, error)
	DecodeBytes(encoded []byte) ([]byte, error)
	DecodeStream(streamObj *PdfObjectStream) ([]byte, error)
	MakeEncodingDict() *PdfObjectDictionary
}

// Flate encoding.
type FlateEncoder struct {
	Predictor        int
	BitsPerComponent int
	// For predictors
	Columns int
	Colors  int
}

// Make a new flate encoder with default parameters, predictor 1 and bits per component 8.
func NewFlateEncoder() *FlateEncoder {
	encoder := &FlateEncoder{}

	// Default (No prediction)
	encoder.Predictor = 1

	// Currently only supporting 8.
	encoder.BitsPerComponent = 8

	encoder.Colors = 1
	encoder.Columns = 1

	return encoder
}

// Make a new instance of an encoding dictionary for a stream object.
// Has the Filter set and the DecodeParms.
func (this *FlateEncoder) MakeEncodingDict() *PdfObjectDictionary {
	dict := PdfObjectDictionary{}

	dict["Filter"] = MakeName("FlateDecode")

	if this.Predictor > 1 {
		decodeParams := PdfObjectDictionary{}
		decodeParams["Predictor"] = MakeInteger(int64(this.Predictor))
		decodeParams["BitsPerComponent"] = MakeInteger(int64(this.BitsPerComponent))
		decodeParams["Columns"] = MakeInteger(int64(this.Columns))
		decodeParams["Colors"] = MakeInteger(int64(this.BitsPerComponent))
		dict["DecodeParms"] = &decodeParams
	}

	return &dict
}

// Create a new flate decoder from a stream object, getting all the encoding parameters
// from the DecodeParms stream object dictionary entry.
func newFlateEncoderFromStream(streamObj *PdfObjectStream) (*FlateEncoder, error) {
	encoder := NewFlateEncoder()

	encDict := streamObj.PdfObjectDictionary
	if encDict == nil {
		// No encoding dictionary.
		return encoder, nil
	}

	obj := (*encDict)["DecodeParms"]
	if obj == nil {
		// No decode parameters (optional).
		return encoder, nil
	}

	decodeParams, isDict := obj.(*PdfObjectDictionary)
	if !isDict {
		common.Log.Debug("Error: DecodeParms not a dictionary (%T)", obj)
		// No decode parameters.
		return nil, fmt.Errorf("Invalid DecodeParms")
	}

	common.Log.Debug("decode params: %s", decodeParams.String())
	obj, has := (*decodeParams)["Predictor"]
	if !has {
		common.Log.Debug("Error: Predictor missing from DecodeParms - Continue with default (1)")
	} else {
		predictor, ok := obj.(*PdfObjectInteger)
		if !ok {
			common.Log.Debug("Error: Predictor specified but not numeric (%T)", obj)
			return nil, fmt.Errorf("Invalid Predictor")
		}
		encoder.Predictor = int(*predictor)
	}

	// Bits per component.  Use default if not specified (8).
	obj, has = (*decodeParams)["BitsPerComponent"]
	if has {
		bpc, ok := obj.(*PdfObjectInteger)
		if !ok {
			common.Log.Debug("ERROR: Invalid BitsPerComponent")
			return nil, fmt.Errorf("Invalid BitsPerComponent")
		}
		encoder.BitsPerComponent = int(*bpc)
	}

	if encoder.Predictor > 1 {
		// Columns.
		encoder.Columns = 1
		obj, has = (*decodeParams)["Columns"]
		if has {
			columns, ok := obj.(*PdfObjectInteger)
			if !ok {
				return nil, fmt.Errorf("Predictor column invalid")
			}

			encoder.Columns = int(*columns)
		}

		// Colors.
		// Number of interleaved color components per sample (Default 1 if not specified)
		encoder.Colors = 1
		obj, has = (*decodeParams)["Colors"]
		if has {
			colors, ok := obj.(*PdfObjectInteger)
			if !ok {
				return nil, fmt.Errorf("Predictor colors not an integer")
			}
			encoder.Colors = int(*colors)
		}
	}

	return encoder, nil
}

func (this *FlateEncoder) DecodeBytes(encoded []byte) ([]byte, error) {
	bufReader := bytes.NewReader(encoded)
	r, err := zlib.NewReader(bufReader)
	if err != nil {
		common.Log.Debug("Decoding error %v\n", err)
		common.Log.Debug("Stream (%d) % x", len(encoded), encoded)
		return nil, err
	}
	defer r.Close()

	var outBuf bytes.Buffer
	outBuf.ReadFrom(r)
	return outBuf.Bytes(), nil
}

// Decode a FlateEncoded stream object and give back decoded bytes.
func (this *FlateEncoder) DecodeStream(streamObj *PdfObjectStream) ([]byte, error) {
	// TODO: Revamp this support to handle TIFF predictor (2).
	// Also handle more filter bytes and support more values of BitsPerComponent.

	common.Log.Debug("FlateDecode")
	common.Log.Debug("Predictor: %d", this.Predictor)
	if this.BitsPerComponent != 8 {
		return nil, fmt.Errorf("Invalid BitsPerComponent (only 8 supported)")
	}

	outData, err := this.DecodeBytes(streamObj.Stream)
	if err != nil {
		return nil, err
	}
	common.Log.Debug("En: % x\n", streamObj.Stream)
	common.Log.Debug("De: % x\n", outData)

	if this.Predictor > 1 {
		if this.Predictor == 2 { // TIFF encoding: Needs some tests.
			common.Log.Debug("Tiff encoding")
			common.Log.Debug("Colors: %d", this.Colors)

			rowLength := int(this.Columns) * this.Colors
			rows := len(outData) / rowLength
			if len(outData)%rowLength != 0 {
				common.Log.Debug("ERROR: TIFF encoding: Invalid row length...")
				return nil, fmt.Errorf("Invalid row length (%d/%d)", len(outData), rowLength)
			}

			if rowLength%this.Colors != 0 {
				return nil, fmt.Errorf("Invalid row length (%d) for colors %d", rowLength, this.Colors)
			}
			common.Log.Debug("inp outData (%d): % x", len(outData), outData)

			pOutBuffer := bytes.NewBuffer(nil)

			// 0-255  -255 255 ; 0-255=-255;
			for i := 0; i < rows; i++ {
				rowData := outData[rowLength*i : rowLength*(i+1)]
				//common.Log.Debug("RowData before: % d", rowData)
				// Predicts the same as the sample to the left.
				// Interleaved by colors.
				for j := this.Colors; j < rowLength; j++ {
					rowData[j] = byte(int(rowData[j]+rowData[j-this.Colors]) % 256)
				}
				pOutBuffer.Write(rowData)
			}
			pOutData := pOutBuffer.Bytes()
			common.Log.Debug("POutData (%d): % x", len(pOutData), pOutData)
			return pOutData, nil
		} else if this.Predictor >= 10 && this.Predictor <= 15 {
			common.Log.Debug("PNG Encoding")
			rowLength := int(this.Columns + 1) // 1 byte to specify predictor algorithms per row.
			rows := len(outData) / rowLength
			if len(outData)%rowLength != 0 {
				common.Log.Debug("ERROR: Invalid row length...")
				return nil, fmt.Errorf("Invalid row length (%d/%d)", len(outData), rowLength)
			}

			pOutBuffer := bytes.NewBuffer(nil)

			common.Log.Debug("Predictor columns: %d", this.Columns)
			common.Log.Debug("Length: %d / %d = %d rows", len(outData), rowLength, rows)
			prevRowData := make([]byte, rowLength)
			for i := 0; i < rowLength; i++ {
				prevRowData[i] = 0
			}

			for i := 0; i < rows; i++ {
				rowData := outData[rowLength*i : rowLength*(i+1)]

				fb := rowData[0]
				switch fb {
				case 0:
					// No prediction. (No operation).
				case 1:
					// Sub: Predicts the same as the sample to the left.
					for j := 2; j < rowLength; j++ {
						rowData[j] = byte(int(rowData[j]+rowData[j-1]) % 256)
					}
				case 2:
					// Up: Predicts the same as the sample above
					for j := 1; j < rowLength; j++ {
						rowData[j] = byte(int(rowData[j]+prevRowData[j]) % 256)
					}
				default:
					common.Log.Debug("ERROR: Invalid filter byte (%d)", fb)
					return nil, fmt.Errorf("Invalid filter byte (%d)", fb)
				}

				for i := 0; i < rowLength; i++ {
					prevRowData[i] = rowData[i]
				}
				pOutBuffer.Write(rowData[1:])
			}
			pOutData := pOutBuffer.Bytes()
			return pOutData, nil
		} else {
			common.Log.Debug("ERROR: Unsupported predictor (%d)", this.Predictor)
			return nil, fmt.Errorf("Unsupported predictor (%d)", this.Predictor)
		}
	}

	return outData, nil
}

// Encode a bytes array and return the encoded value based on the encoder parameters.
func (this *FlateEncoder) EncodeBytes(data []byte) ([]byte, error) {
	if this.Predictor != 1 {
		return nil, fmt.Errorf("FlateEncoder Predictor = 1 only supported yet")
	}

	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write(data)
	w.Close()

	return b.Bytes(), nil
}

// LZW encoding/decoding functionality.
type LZWEncoder struct {
	Predictor        int
	BitsPerComponent int
	// For predictors
	Columns int
	Colors  int
	// LZW algorithm setting.
	EarlyChange int
}

// Make a new LZW encoder with default parameters.
func NewLZWEncoder() *LZWEncoder {
	encoder := &LZWEncoder{}

	// Default (No prediction)
	encoder.Predictor = 1

	// Currently only supporting 8.
	encoder.BitsPerComponent = 8

	encoder.Colors = 1
	encoder.Columns = 1
	encoder.EarlyChange = 1

	return encoder
}

// Make a new instance of an encoding dictionary for a stream object.
// Has the Filter set and the DecodeParms.
func (this *LZWEncoder) MakeEncodingDict() *PdfObjectDictionary {
	dict := PdfObjectDictionary{}

	dict["Filter"] = MakeName("LZWDecode")

	if this.Predictor > 1 {
		decodeParams := PdfObjectDictionary{}
		decodeParams["Predictor"] = MakeInteger(int64(this.Predictor))
		decodeParams["BitsPerComponent"] = MakeInteger(int64(this.BitsPerComponent))
		decodeParams["Columns"] = MakeInteger(int64(this.Columns))
		decodeParams["Colors"] = MakeInteger(int64(this.BitsPerComponent))
		dict["DecodeParms"] = &decodeParams
	}

	dict["EarlyChange"] = MakeInteger(int64(this.EarlyChange))

	return &dict
}

// Create a new LZW encoder/decoder from a stream object, getting all the encoding parameters
// from the DecodeParms stream object dictionary entry.
func newLZWEncoderFromStream(streamObj *PdfObjectStream) (*LZWEncoder, error) {
	// Start with default settings.
	encoder := NewLZWEncoder()

	encDict := streamObj.PdfObjectDictionary
	if encDict == nil {
		// No encoding dictionary.
		return encoder, nil
	}

	obj := (*encDict)["DecodeParms"]
	if obj == nil {
		// No decode parameters (optional).
		return encoder, nil
	}

	decodeParams, isDict := obj.(*PdfObjectDictionary)
	if !isDict {
		common.Log.Debug("Error: DecodeParms not a dictionary (%T)", obj)
		return nil, fmt.Errorf("Invalid DecodeParms")
	}

	// The EarlyChange indicates when to increase code length, as different
	// implementations use a different mechanisms. Essentially this chooses
	// which LZW implementation to use.
	// The default is 1 (one code early)
	obj, has := (*decodeParams)["EarlyChange"]
	if has {
		earlyChange, ok := obj.(*PdfObjectInteger)
		if !ok {
			common.Log.Debug("Error: EarlyChange specified but not numeric (%T)", obj)
			return nil, fmt.Errorf("Invalid EarlyChange")
		}
		if *earlyChange != 0 && *earlyChange != 1 {
			return nil, fmt.Errorf("Invalid EarlyChange value (not 0 or 1)")
		}

		encoder.EarlyChange = int(*earlyChange)
	}

	obj, has = (*decodeParams)["Predictor"]
	if has {
		predictor, ok := obj.(*PdfObjectInteger)
		if !ok {
			common.Log.Debug("Error: Predictor specified but not numeric (%T)", obj)
			return nil, fmt.Errorf("Invalid Predictor")
		}
		encoder.Predictor = int(*predictor)
	}

	// Bits per component.  Use default if not specified (8).
	obj, has = (*decodeParams)["BitsPerComponent"]
	if has {
		bpc, ok := obj.(*PdfObjectInteger)
		if !ok {
			common.Log.Debug("ERROR: Invalid BitsPerComponent")
			return nil, fmt.Errorf("Invalid BitsPerComponent")
		}
		encoder.BitsPerComponent = int(*bpc)
	}

	if encoder.Predictor > 1 {
		// Columns.
		encoder.Columns = 1
		obj, has = (*decodeParams)["Columns"]
		if has {
			columns, ok := obj.(*PdfObjectInteger)
			if !ok {
				return nil, fmt.Errorf("Predictor column invalid")
			}

			encoder.Columns = int(*columns)
		}

		// Colors.
		// Number of interleaved color components per sample (Default 1 if not specified)
		encoder.Colors = 1
		obj, has = (*decodeParams)["Colors"]
		if has {
			colors, ok := obj.(*PdfObjectInteger)
			if !ok {
				return nil, fmt.Errorf("Predictor colors not an integer")
			}
			encoder.Colors = int(*colors)
		}
	}

	common.Log.Debug("decode params: %s", decodeParams.String())
	return encoder, nil
}

func (this *LZWEncoder) DecodeBytes(encoded []byte) ([]byte, error) {
	var outBuf bytes.Buffer
	bufReader := bytes.NewReader(encoded)

	var r io.ReadCloser
	if this.EarlyChange == 1 {
		// LZW implementation with code length increases one code early (1).
		r = lzw1.NewReader(bufReader, lzw1.MSB, 8)
	} else {
		// 0: LZW implementation with postponed code length increases (0).
		r = lzw0.NewReader(bufReader, lzw0.MSB, 8)
	}
	defer r.Close()

	_, err := outBuf.ReadFrom(r)
	if err != nil {
		return nil, err
	}

	return outBuf.Bytes(), nil
}

func (this *LZWEncoder) DecodeStream(streamObj *PdfObjectStream) ([]byte, error) {
	// Revamp this support to handle TIFF predictor (2).
	// Also handle more filter bytes and check
	// BitsPerComponent.  Default value is 8, currently we are only
	// supporting that one.

	common.Log.Debug("LZW Decoding")
	common.Log.Debug("Predictor: %d", this.Predictor)

	outData, err := this.DecodeBytes(streamObj.Stream)
	if err != nil {
		return nil, err
	}

	common.Log.Debug(" IN: (%d) % x", len(streamObj.Stream), streamObj.Stream)
	common.Log.Debug("OUT: (%d) % x", len(outData), outData)

	if this.Predictor > 1 {
		if this.Predictor == 2 { // TIFF encoding: Needs some tests.
			common.Log.Debug("Tiff encoding")

			rowLength := int(this.Columns) * this.Colors
			rows := len(outData) / rowLength
			if len(outData)%rowLength != 0 {
				common.Log.Debug("ERROR: TIFF encoding: Invalid row length...")
				return nil, fmt.Errorf("Invalid row length (%d/%d)", len(outData), rowLength)
			}

			if rowLength%this.Colors != 0 {
				return nil, fmt.Errorf("Invalid row length (%d) for colors %d", rowLength, this.Colors)
			}
			common.Log.Debug("inp outData (%d): % x", len(outData), outData)

			pOutBuffer := bytes.NewBuffer(nil)

			// 0-255  -255 255 ; 0-255=-255;
			for i := 0; i < rows; i++ {
				rowData := outData[rowLength*i : rowLength*(i+1)]
				//common.Log.Debug("RowData before: % d", rowData)
				// Predicts the same as the sample to the left.
				// Interleaved by colors.
				for j := this.Colors; j < rowLength; j++ {
					rowData[j] = byte(int(rowData[j]+rowData[j-this.Colors]) % 256)
				}
				// GH: Appears that this is not working as expected...

				pOutBuffer.Write(rowData)
			}
			pOutData := pOutBuffer.Bytes()
			common.Log.Debug("POutData (%d): % x", len(pOutData), pOutData)
			return pOutData, nil
		} else if this.Predictor >= 10 && this.Predictor <= 15 {
			common.Log.Debug("PNG Encoding")
			rowLength := int(this.Columns + 1) // 1 byte to specify predictor algorithms per row.
			rows := len(outData) / rowLength
			if len(outData)%rowLength != 0 {
				common.Log.Debug("ERROR: Invalid row length...")
				return nil, fmt.Errorf("Invalid row length (%d/%d)", len(outData), rowLength)
			}

			pOutBuffer := bytes.NewBuffer(nil)

			common.Log.Debug("Predictor columns: %d", this.Columns)
			common.Log.Debug("Length: %d / %d = %d rows", len(outData), rowLength, rows)
			prevRowData := make([]byte, rowLength)
			for i := 0; i < rowLength; i++ {
				prevRowData[i] = 0
			}

			for i := 0; i < rows; i++ {
				rowData := outData[rowLength*i : rowLength*(i+1)]

				fb := rowData[0]
				switch fb {
				case 0:
					// No prediction. (No operation).
				case 1:
					// Sub: Predicts the same as the sample to the left.
					for j := 2; j < rowLength; j++ {
						rowData[j] = byte(int(rowData[j]+rowData[j-1]) % 256)
					}
				case 2:
					// Up: Predicts the same as the sample above
					for j := 1; j < rowLength; j++ {
						rowData[j] = byte(int(rowData[j]+prevRowData[j]) % 256)
					}
				default:
					common.Log.Debug("ERROR: Invalid filter byte (%d)", fb)
					return nil, fmt.Errorf("Invalid filter byte (%d)", fb)
				}

				for i := 0; i < rowLength; i++ {
					prevRowData[i] = rowData[i]
				}
				pOutBuffer.Write(rowData[1:])
			}
			pOutData := pOutBuffer.Bytes()
			return pOutData, nil
		} else {
			common.Log.Debug("ERROR: Unsupported predictor (%d)", this.Predictor)
			return nil, fmt.Errorf("Unsupported predictor (%d)", this.Predictor)
		}
	}

	return outData, nil
}

// Support for encoding LZW.  Currently not supporting predictors (raw compressed data only).
// Only supports the Early change = 1 algorithm (compress/lzw) as the other implementation
// does not have a write method.
// TODO: Consider refactoring compress/lzw to allow both.
func (this *LZWEncoder) EncodeBytes(data []byte) ([]byte, error) {
	if this.Predictor != 1 {
		return nil, fmt.Errorf("LZW Predictor = 1 only supported yet")
	}

	if this.EarlyChange == 1 {
		return nil, fmt.Errorf("LZW Early Change = 0 only supported yet")
	}

	var b bytes.Buffer
	w := lzw0.NewWriter(&b, lzw0.MSB, 8)
	w.Write(data)
	w.Close()

	return b.Bytes(), nil
}

/////
// ASCII hex encoder/decoder.
type ASCIIHexEncoder struct {
}

// Make a new LZW encoder with default parameters.
func NewASCIIHexEncoder() *ASCIIHexEncoder {
	encoder := &ASCIIHexEncoder{}
	return encoder
}

// Make a new instance of an encoding dictionary for a stream object.
// Has the Filter set and the DecodeParms.
func (this *ASCIIHexEncoder) MakeEncodingDict() *PdfObjectDictionary {
	dict := PdfObjectDictionary{}

	dict["Filter"] = MakeName("ASCIIHexDecode")
	return &dict
}

func (this *ASCIIHexEncoder) DecodeBytes(encoded []byte) ([]byte, error) {
	bufReader := bytes.NewReader(encoded)
	inb := []byte{}
	for {
		b, err := bufReader.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == '>' {
			break
		}
		if IsWhiteSpace(b) {
			continue
		}
		if (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F') || (b >= '0' && b <= '9') {
			inb = append(inb, b)
		} else {
			common.Log.Debug("ERROR: Invalid ascii hex character (%c)", b)
			return nil, fmt.Errorf("Invalid ascii hex character (%c)", b)
		}
	}
	if len(inb)%2 == 1 {
		inb = append(inb, '0')
	}
	common.Log.Debug("Inbound %s", inb)
	outb := make([]byte, hex.DecodedLen(len(inb)))
	_, err := hex.Decode(outb, inb)
	if err != nil {
		return nil, err
	}
	return outb, nil
}

// ASCII hex decoding.
func (this *ASCIIHexEncoder) DecodeStream(streamObj *PdfObjectStream) ([]byte, error) {
	return this.DecodeBytes(streamObj.Stream)
}

func (this *ASCIIHexEncoder) EncodeBytes(data []byte) ([]byte, error) {
	var encoded bytes.Buffer

	for _, b := range data {
		encoded.WriteString(fmt.Sprintf("%.2X ", b))
	}
	encoded.WriteByte('>')

	return encoded.Bytes(), nil
}

//
// Raw encoder/decoder (no encoding, pass through)
//
type RawEncoder struct{}

func NewRawEncoder() *RawEncoder {
	return &RawEncoder{}
}

func (this *RawEncoder) MakeEncodingDict() *PdfObjectDictionary {
	return &PdfObjectDictionary{}
}

func (this *RawEncoder) DecodeBytes(encoded []byte) ([]byte, error) {
	return encoded, nil
}

func (this *RawEncoder) DecodeStream(streamObj *PdfObjectStream) ([]byte, error) {
	return streamObj.Stream, nil
}

func (this *RawEncoder) EncodeBytes(data []byte) ([]byte, error) {
	return data, nil
}